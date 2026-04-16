package api

import (
	"context"
	"net/http"
	"strings"
)

// contextKey is a package-private type for request context keys, preventing collisions.
type contextKey string

const userIDKey contextKey = "user_id"

// extractBearerToken returns the token from "Authorization: Bearer <token>", or "".
func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

// corsMiddleware adds CORS headers for local Electron development.
// Reflects the request Origin so credentials (cookies) can be sent — wildcard origins
// are incompatible with credentials: 'include'.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			origin = "http://localhost:5173"
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth validates the access token (Bearer header or cookie) and injects the caller's
// user ID into the request context. Returns 401 if the token is missing or invalid.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractBearerToken(r)
		if tokenStr == "" {
			if cookie, err := r.Cookie(cookieAccessToken); err == nil {
				tokenStr = cookie.Value
			}
		}
		if tokenStr == "" {
			writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "authentication required"})
			return
		}
		claims, err := h.sessionSvc.ValidateAccessToken(tokenStr)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid or expired token"})
			return
		}
		ctx := context.WithValue(r.Context(), userIDKey, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// userIDFromContext retrieves the authenticated user's ID from the request context.
// Panics if called outside of a requireAuth-protected route (programming error).
func userIDFromContext(ctx context.Context) int64 {
	return ctx.Value(userIDKey).(int64)
}
