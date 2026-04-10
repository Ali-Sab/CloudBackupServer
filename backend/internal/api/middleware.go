package api

import (
	"context"
	"net/http"
)

// contextKey is a package-private type for request context keys, preventing collisions.
type contextKey string

const userIDKey contextKey = "user_id"

// corsMiddleware adds permissive CORS headers suitable for local Electron development.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth validates the Bearer token and injects the caller's user ID into the
// request context. Returns 401 if the token is missing or invalid.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearerToken(r)
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "authentication required"})
			return
		}
		claims, err := h.sessionSvc.ValidateAccessToken(token)
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
