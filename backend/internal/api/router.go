package api

import (
	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
	"github.com/ali-sab/cloudbackupserver/backend/internal/storage"
)

// NewTestRouter is like NewRouter but uses bcrypt.MinCost for faster tests.
func NewTestRouter(pool *pgxpool.Pool, sessionSvc *session.Service, store storage.Backend) *chi.Mux {
	return newRouter(newTestHandler(pool, sessionSvc, store))
}

// NewRouter wires up all routes and middleware and returns the root handler.
func NewRouter(pool *pgxpool.Pool, sessionSvc *session.Service, store storage.Backend) *chi.Mux {
	return newRouter(NewHandler(pool, sessionSvc, store))
}

func newRouter(h *Handler) *chi.Mux {

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

		r.Route("/folders", func(r chi.Router) {
			r.Use(h.requireAuth)
			r.Get("/", h.GetFolders)
			r.Post("/", h.PostFolder)
			r.Route("/{folderID}", func(r chi.Router) {
				r.Delete("/", h.DeleteFolder)
				r.Put("/", h.PutFolder)
				r.Get("/files", h.GetFiles)
				r.Put("/sync", h.PutSyncFiles)
				r.Get("/backups", h.GetFileBackups)
				r.Put("/backup/*", h.PutFileBackup)
				r.Get("/backup/*", h.GetFileBackup)
				r.Get("/versions", h.GetFileVersions)
				r.Get("/versions/{versionID}", h.GetFileVersionDownload)
			})
		})

		r.Route("/account", func(r chi.Router) {
			r.Use(h.requireAuth)
			r.Put("/email", h.PutAccountEmail)
			r.Put("/password", h.PutAccountPassword)
			r.Delete("/", h.DeleteAccount)
		})

		r.With(h.requireAuth).Get("/history", h.GetBackupHistory)
	})

	return r
}
