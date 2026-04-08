package api

import (
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
)

// NewRouter wires up all routes and middleware and returns the root handler.
func NewRouter(pool *pgxpool.Pool, sessionSvc *session.Service) *chi.Mux {
	h := NewHandler(pool, sessionSvc)

	r := chi.NewRouter()

	// Global middleware
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(corsMiddleware)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", h.GetHealth)
		r.Get("/session", h.GetSession)

		r.Route("/auth", func(r chi.Router) {
			r.Post("/login", h.PostLogin)
			r.Post("/register", h.PostRegister)
			r.Post("/refresh", h.PostRefresh)
			r.Post("/logout", h.PostLogout)
			r.Post("/forgot-password", h.PostForgotPassword)
			r.Post("/reset-password", h.PostResetPassword)
		})
	})

	return r
}
