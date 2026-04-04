package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ali-sab/cloudbackupserver/backend/internal/api"
	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
)

func main() {
	databaseURL := mustEnv("DATABASE_URL")
	jwtSecret := mustEnv("JWT_SECRET")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// Run database migrations (idempotent — safe on every startup)
	log.Println("Running database migrations...")
	if err := db.RunMigrations(databaseURL); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}
	log.Println("Migrations complete")

	// Connect to the database
	ctx := context.Background()
	pool, err := db.Connect(ctx, databaseURL)
	if err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}
	defer pool.Close()

	// Wire up application
	sessionSvc := session.NewService(jwtSecret)
	router := api.NewRouter(pool, sessionSvc)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in background
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Server listening on :%s", port)
		serverErr <- srv.ListenAndServe()
	}()

	// Block until interrupt or server error
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

func mustEnv(key string) string {
	val := os.Getenv(key)
	if val == "" {
		log.Fatalf("Required environment variable %s is not set", key)
	}
	return val
}
