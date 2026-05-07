package api

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// contextKey is a package-private type for request context keys, preventing collisions.
type contextKey string

const userIDKey contextKey = "user_id"

// allowedOrigins holds the CORS / CSRF origin allowlist (set via SetAllowedOrigins).
// Defaults to the local Electron renderer URL so unit tests work without explicit config.
var (
	allowedOriginsMu sync.RWMutex
	allowedOrigins   = map[string]struct{}{
		"http://localhost:5173": {},
	}
)

// SetAllowedOrigins replaces the CORS / CSRF origin allowlist.
// Pass a comma-separated value from env (e.g. "http://localhost:5173,https://app.example.com").
// Empty / whitespace-only entries are ignored.
func SetAllowedOrigins(csv string) {
	next := make(map[string]struct{})
	for _, s := range strings.Split(csv, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		next[s] = struct{}{}
	}
	if len(next) == 0 {
		return // keep default
	}
	allowedOriginsMu.Lock()
	allowedOrigins = next
	allowedOriginsMu.Unlock()
}

func isAllowedOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	allowedOriginsMu.RLock()
	defer allowedOriginsMu.RUnlock()
	_, ok := allowedOrigins[origin]
	return ok
}

// corsMiddleware enforces a strict CORS allowlist with credentialed requests.
// Reflects the request Origin only when it appears in the allowlist; otherwise
// no Access-Control-Allow-Origin header is set, which causes the browser to
// reject the response.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isAllowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Checksum-SHA256, X-File-Size, X-Restored-From-Version-ID")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrfMiddleware blocks state-changing requests whose Origin (or Referer
// fallback) is not in the allowlist. With SameSite=Strict cookies this is
// belt-and-suspenders, but cheap insurance.
//
// Safe methods (GET, HEAD, OPTIONS) are always allowed — they don't change
// state, and many tools / RSS readers / etc. don't send Origin on GET.
func csrfMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Some clients only send Referer. Extract origin from it.
			if ref := r.Header.Get("Referer"); ref != "" {
				if u, err := url.Parse(ref); err == nil && u.Scheme != "" && u.Host != "" {
					origin = u.Scheme + "://" + u.Host
				}
			}
		}
		if !isAllowedOrigin(origin) {
			writeJSON(w, http.StatusForbidden, ErrorResponse{Error: "cross-origin request rejected"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth validates the access_token cookie and injects the caller's
// user ID into the request context. Returns 401 if the cookie is missing or invalid.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieAccessToken)
		if err != nil || cookie.Value == "" {
			writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "authentication required"})
			return
		}
		claims, err := h.sessionSvc.ValidateAccessToken(cookie.Value)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid or expired token"})
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// userIDFromContext retrieves the authenticated user's ID from the request context.
// Returns 0 if not present (callers paired with requireAuth should never see this).
func userIDFromContext(ctx context.Context) int64 {
	v, ok := ctx.Value(userIDKey).(int64)
	if !ok {
		return 0
	}
	return v
}

// ---- Per-IP rate limiter for unauthenticated auth endpoints ----
//
// Token-bucket per IP, in-memory. Good enough for a single-instance backend
// to slow down credential-stuffing and registration floods. For multi-instance
// deployments this would move to Redis.

type rateLimiter struct {
	disabled bool
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     float64       // tokens per second
	capacity float64       // burst size
	ttl      time.Duration // evict idle buckets after this
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

func newRateLimiter(perMinute, burst int, disabled bool) *rateLimiter {
	return &rateLimiter{
		disabled: disabled,
		buckets:  make(map[string]*bucket),
		rate:     float64(perMinute) / 60.0,
		capacity: float64(burst),
		ttl:      10 * time.Minute,
	}
}

// allow returns true if a request from `key` is permitted right now.
func (rl *rateLimiter) allow(key string) bool {
	if rl.disabled {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.buckets[key]
	if !ok {
		rl.buckets[key] = &bucket{tokens: rl.capacity - 1, lastRefill: now}
		return true
	}
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens = min64(rl.capacity, b.tokens+elapsed*rl.rate)
	b.lastRefill = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	// Lazy GC of idle entries.
	if len(rl.buckets) > 1024 {
		for k, bk := range rl.buckets {
			if now.Sub(bk.lastRefill) > rl.ttl {
				delete(rl.buckets, k)
			}
		}
	}
	return true
}

func min64(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// isLoopbackIP reports whether an IP string (with or without brackets) is a
// loopback address. Handles 127.x.x.x, ::1, and [::1] bracket notation.
func isLoopbackIP(ip string) bool {
	// strip brackets: "[::1]" → "::1"
	if strings.HasPrefix(ip, "[") && strings.HasSuffix(ip, "]") {
		ip = ip[1 : len(ip)-1]
	}
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}

// rateLimit returns a middleware that limits requests per remote IP.
// Loopback addresses (127.x.x.x, ::1) bypass the limiter — they're only
// reachable locally and represent E2E tests / dev tooling, not external threats.
func rateLimit(rl *rateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := clientIP(r)
			if isLoopbackIP(ip) {
				next.ServeHTTP(w, r)
				return
			}
			if !rl.allow(ip) {
				w.Header().Set("Retry-After", "60")
				writeJSON(w, http.StatusTooManyRequests, ErrorResponse{Error: "too many requests — please slow down"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	// chimiddleware.RealIP already populates RemoteAddr from X-Forwarded-For when trusted.
	addr := r.RemoteAddr
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}
