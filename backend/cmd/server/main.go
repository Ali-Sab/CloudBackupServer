package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ali-sab/cloudbackupserver/backend/internal/api"
	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
	"github.com/ali-sab/cloudbackupserver/backend/internal/storage"
)

func main() {
	databaseURL := mustEnv("DATABASE_URL")
	jwtSecret := mustEnv("JWT_SECRET")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Tighten CORS / CSRF allowlist. Defaults to http://localhost:5173 (dev).
	if origins := os.Getenv("ALLOWED_ORIGINS"); origins != "" {
		api.SetAllowedOrigins(origins)
	}

	// COOKIE_SECURE flips Set-Cookie's Secure flag; must be true behind HTTPS.
	api.SetSecureCookies(os.Getenv("COOKIE_SECURE") == "true")

	log.Println("Running database migrations...")
	if err := db.RunMigrations(databaseURL); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}
	log.Println("Migrations complete")

	ctx := context.Background()
	pool, err := db.Connect(ctx, databaseURL)
	if err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}
	defer pool.Close()

	store, err := storage.New(
		mustEnv("MINIO_ENDPOINT"),
		mustEnv("MINIO_ACCESS_KEY"),
		mustEnv("MINIO_SECRET_KEY"),
		mustEnv("MINIO_BUCKET"),
		os.Getenv("MINIO_USE_SSL") == "true",
	)
	if err != nil {
		log.Fatalf("Storage init failed: %v", err)
	}

	sessionSvc := session.NewService(jwtSecret)
	router := api.NewRouter(pool, sessionSvc, store)

	// ReadTimeout is intentionally omitted: upload handlers stream multi-GiB bodies and must not
	// be interrupted mid-transfer. ReadHeaderTimeout guards against slow-header attacks, and each
	// handler wraps its body with MaxBytesReader to enforce size limits.
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// Periodic cleanup of expired refresh tokens. Runs once at startup, then hourly.
	cleanupCtx, cancelCleanup := context.WithCancel(context.Background())
	defer cancelCleanup()
	go runRefreshCleanup(cleanupCtx, pool)

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Server listening on :%s", port)
		serverErr <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		log.Fatalf("Server error: %v", err)
	case sig := <-quit:
		log.Printf("Received %v — shutting down gracefully...", sig)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Fatalf("Graceful shutdown failed: %v", err)
	}
	log.Println("Server stopped")
}

func runRefreshCleanup(ctx context.Context, pool *pgxpool.Pool) {
	tick := func() {
		n, err := db.CleanupExpiredRefreshTokens(ctx, pool, 7*24*time.Hour)
		if err != nil {
			log.Printf("warn: refresh token cleanup failed: %v", err)
			return
		}
		if n > 0 {
			log.Printf("info: cleaned up %d expired refresh tokens", n)
		}
	}
	tick()
	t := time.NewTicker(1 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

func mustEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		log.Fatalf("Required environment variable %s is not set", key)
	}
	return val
}
