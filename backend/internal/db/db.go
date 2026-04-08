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
