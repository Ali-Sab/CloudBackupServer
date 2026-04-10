package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/ali-sab/cloudbackupserver/backend/internal/models"
	"github.com/ali-sab/cloudbackupserver/backend/migrations"
)

// ---- Connection & migrations ----

// Connect creates and validates a pgxpool connection pool.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("creating pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}
	return pool, nil
}

// RunMigrations applies all pending SQL migrations using goose.
// Migrations are embedded in the binary and run at every startup (idempotent).
func RunMigrations(databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("opening db for migrations: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(migrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("setting goose dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	return nil
}

// ---- Users ----

// CreateUser inserts a new user row and populates the generated fields (id, timestamps).
func CreateUser(ctx context.Context, pool *pgxpool.Pool, user *models.User) error {
	err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash)
		 VALUES ($1, $2)
		 RETURNING id, created_at, updated_at`,
		user.Email, user.PasswordHash,
	).Scan(&user.ID, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return fmt.Errorf("creating user: %w", err)
	}
	return nil
}

// GetUserByEmail returns the user with the given email, or an error if not found.
func GetUserByEmail(ctx context.Context, pool *pgxpool.Pool, email string) (*models.User, error) {
	u := &models.User{}
	err := pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at, updated_at
		 FROM users WHERE email = $1`,
		email,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting user by email: %w", err)
	}
	return u, nil
}

// GetUserByID returns the user with the given id, or an error if not found.
func GetUserByID(ctx context.Context, pool *pgxpool.Pool, id int64) (*models.User, error) {
	u := &models.User{}
	err := pool.QueryRow(ctx,
		`SELECT id, email, password_hash, created_at, updated_at
		 FROM users WHERE id = $1`,
		id,
	).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting user by id: %w", err)
	}
	return u, nil
}

// UpdateUserPassword sets a new bcrypt password hash for the given user.
func UpdateUserPassword(ctx context.Context, pool *pgxpool.Pool, userID int64, hash string) error {
	_, err := pool.Exec(ctx,
		`UPDATE users SET password_hash = $1, updated_at = NOW() WHERE id = $2`,
		hash, userID,
	)
	if err != nil {
		return fmt.Errorf("updating user password: %w", err)
	}
	return nil
}

// ---- Refresh tokens ----

// CreateRefreshToken inserts a new refresh token row.
func CreateRefreshToken(ctx context.Context, pool *pgxpool.Pool, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO refresh_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("creating refresh token: %w", err)
	}
	return nil
}

// GetRefreshTokenByHash returns the refresh token row matching the given hash.
func GetRefreshTokenByHash(ctx context.Context, pool *pgxpool.Pool, hash string) (*models.RefreshToken, error) {
	rt := &models.RefreshToken{}
	err := pool.QueryRow(ctx,
		`SELECT id, user_id, token_hash, expires_at, revoked, created_at
		 FROM refresh_tokens WHERE token_hash = $1`,
		hash,
	).Scan(&rt.ID, &rt.UserID, &rt.TokenHash, &rt.ExpiresAt, &rt.Revoked, &rt.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting refresh token: %w", err)
	}
	return rt, nil
}

// RevokeRefreshToken marks a single refresh token as revoked.
func RevokeRefreshToken(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	_, err := pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = TRUE WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("revoking refresh token: %w", err)
	}
	return nil
}

// RevokeAllUserRefreshTokens revokes every refresh token belonging to a user.
// Called on logout-all, password reset, and theft detection.
func RevokeAllUserRefreshTokens(ctx context.Context, pool *pgxpool.Pool, userID int64) error {
	_, err := pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked = TRUE WHERE user_id = $1 AND revoked = FALSE`,
		userID,
	)
	if err != nil {
		return fmt.Errorf("revoking all user refresh tokens: %w", err)
	}
	return nil
}

// ---- Password reset tokens ----

// CreatePasswordResetToken inserts a new password-reset token row.
func CreatePasswordResetToken(ctx context.Context, pool *pgxpool.Pool, userID int64, tokenHash string, expiresAt time.Time) error {
	_, err := pool.Exec(ctx,
		`INSERT INTO password_reset_tokens (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`,
		userID, tokenHash, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("creating password reset token: %w", err)
	}
	return nil
}

// GetPasswordResetTokenByHash returns the password-reset token matching the given hash.
func GetPasswordResetTokenByHash(ctx context.Context, pool *pgxpool.Pool, hash string) (*models.PasswordResetToken, error) {
	prt := &models.PasswordResetToken{}
	err := pool.QueryRow(ctx,
		`SELECT id, user_id, token_hash, expires_at, used, created_at
		 FROM password_reset_tokens WHERE token_hash = $1`,
		hash,
	).Scan(&prt.ID, &prt.UserID, &prt.TokenHash, &prt.ExpiresAt, &prt.Used, &prt.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting password reset token: %w", err)
	}
	return prt, nil
}

// MarkPasswordResetTokenUsed marks a reset token as consumed so it cannot be reused.
func MarkPasswordResetTokenUsed(ctx context.Context, pool *pgxpool.Pool, id int64) error {
	_, err := pool.Exec(ctx,
		`UPDATE password_reset_tokens SET used = TRUE WHERE id = $1`,
		id,
	)
	if err != nil {
		return fmt.Errorf("marking password reset token used: %w", err)
	}
	return nil
}

// ---- Watched paths ----

// SetWatchedPath upserts the user's watched directory path.
// Each user may have at most one watched path; this replaces it if it already exists.
// If the path changes, the stale file list is cleared atomically in the same transaction.
func SetWatchedPath(ctx context.Context, pool *pgxpool.Pool, userID int64, path string) (*models.WatchedPath, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// Snapshot the existing path (if any) before the upsert.
	var oldPathID int64
	var oldPath string
	hadExisting := true
	if err := tx.QueryRow(ctx,
		`SELECT id, path FROM watched_paths WHERE user_id = $1`, userID,
	).Scan(&oldPathID, &oldPath); err != nil {
		hadExisting = false
	}

	wp := &models.WatchedPath{}
	if err := tx.QueryRow(ctx,
		`INSERT INTO watched_paths (user_id, path)
		 VALUES ($1, $2)
		 ON CONFLICT (user_id) DO UPDATE
		   SET path = EXCLUDED.path, updated_at = NOW()
		 RETURNING id, user_id, path, created_at, updated_at`,
		userID, path,
	).Scan(&wp.ID, &wp.UserID, &wp.Path, &wp.CreatedAt, &wp.UpdatedAt); err != nil {
		return nil, fmt.Errorf("setting watched path: %w", err)
	}

	// Clear the file list when the path changes — the old files describe a different directory.
	if hadExisting && oldPath != path {
		if _, err := tx.Exec(ctx,
			`DELETE FROM watched_files WHERE path_id = $1`, oldPathID,
		); err != nil {
			return nil, fmt.Errorf("clearing stale watched files: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing watched path: %w", err)
	}
	return wp, nil
}

// GetWatchedPathByUserID returns the user's watched path, or an error if none is set.
func GetWatchedPathByUserID(ctx context.Context, pool *pgxpool.Pool, userID int64) (*models.WatchedPath, error) {
	wp := &models.WatchedPath{}
	err := pool.QueryRow(ctx,
		`SELECT id, user_id, path, created_at, updated_at
		 FROM watched_paths WHERE user_id = $1`,
		userID,
	).Scan(&wp.ID, &wp.UserID, &wp.Path, &wp.CreatedAt, &wp.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting watched path: %w", err)
	}
	return wp, nil
}

// ---- Watched files ----

// SyncWatchedFiles replaces all file entries for a watched path in a single transaction.
// The incoming slice is the complete current state of the directory.
func SyncWatchedFiles(ctx context.Context, pool *pgxpool.Pool, pathID int64, files []models.WatchedFile) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	if _, err := tx.Exec(ctx, `DELETE FROM watched_files WHERE path_id = $1`, pathID); err != nil {
		return fmt.Errorf("clearing watched files: %w", err)
	}

	for _, f := range files {
		if _, err := tx.Exec(ctx,
			`INSERT INTO watched_files (path_id, name, relative_path, is_directory, size, modified_ms)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			pathID, f.Name, f.RelativePath, f.IsDirectory, f.Size, f.ModifiedMs,
		); err != nil {
			return fmt.Errorf("inserting watched file: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing watched files: %w", err)
	}
	return nil
}

// GetWatchedFiles returns all file entries for the given watched path.
// Results are ordered by relative_path ASC, which gives natural tree order
// (e.g. "a/", "a/b.txt", "c.txt").
func GetWatchedFiles(ctx context.Context, pool *pgxpool.Pool, pathID int64) ([]models.WatchedFile, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, path_id, name, relative_path, is_directory, size, modified_ms, created_at
		 FROM watched_files WHERE path_id = $1
		 ORDER BY relative_path ASC`,
		pathID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying watched files: %w", err)
	}
	defer rows.Close()

	var files []models.WatchedFile
	for rows.Next() {
		var f models.WatchedFile
		if err := rows.Scan(&f.ID, &f.PathID, &f.Name, &f.RelativePath, &f.IsDirectory, &f.Size, &f.ModifiedMs, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning watched file: %w", err)
		}
		files = append(files, f)
	}
	if files == nil {
		files = []models.WatchedFile{}
	}
	return files, rows.Err()
}
