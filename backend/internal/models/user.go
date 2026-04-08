package models

import "time"

// User represents a registered user in the system.
type User struct {
	ID           int64     `json:"id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// RefreshToken represents a stored refresh token (only the hash is persisted).
type RefreshToken struct {
	ID        int64
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
	Revoked   bool
	CreatedAt time.Time
}

// PasswordResetToken represents a single-use password reset token.
type PasswordResetToken struct {
	ID        int64
	UserID    int64
	TokenHash string
	ExpiresAt time.Time
	Used      bool
	CreatedAt time.Time
}
