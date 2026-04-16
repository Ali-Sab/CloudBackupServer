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

// WatchedPath represents a user's chosen local backup directory.
// Each user has at most one watched path (enforced by DB unique constraint).
type WatchedPath struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FileBackup represents a successfully uploaded backup of a single file.
// Records are keyed by (user_id, relative_path) and survive metadata syncs —
// they are only removed when the user changes their watched path.
type FileBackup struct {
	ID             int64     `json:"id"`
	UserID         int64     `json:"user_id"`
	RelativePath   string    `json:"relative_path"`
	Size           int64     `json:"size"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	ObjectKey      string    `json:"object_key"`
	BackedUpAt     time.Time `json:"backed_up_at"`
}

// WatchedFile represents a single file or directory entry within a watched path.
// RelativePath is the POSIX path relative to the watched root (e.g. "photos/2024/img.jpg").
// For top-level entries RelativePath equals Name.
type WatchedFile struct {
	ID           int64     `json:"id"`
	PathID       int64     `json:"path_id"`
	Name         string    `json:"name"`
	RelativePath string    `json:"relative_path"`
	IsDirectory  bool      `json:"is_directory"`
	Size         int64     `json:"size"`
	ModifiedMs   int64     `json:"modified_ms"`
	CreatedAt    time.Time `json:"created_at"`
}
