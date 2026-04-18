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

// WatchedPath represents one of a user's local backup directories.
// A user may have many watched paths (no unique constraint on user_id).
type WatchedPath struct {
	ID        int64     `json:"id"`
	UserID    int64     `json:"user_id"`
	Path      string    `json:"path"`
	Name      string    `json:"name"` // user-visible label; defaults to last path segment
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FolderStats is the dashboard aggregate row for a single watched path.
// Totals are computed from file_backups at query time.
type FolderStats struct {
	ID             int64      `json:"id"`
	Path           string     `json:"path"`
	Name           string     `json:"name"`
	FileCount      int        `json:"file_count"`
	TotalSizeBytes int64      `json:"total_size_bytes"`
	LastBackedUpAt *time.Time `json:"last_backed_up_at"` // nil when nothing has been backed up yet
}

// FileBackup represents a successfully uploaded backup of a single file.
// Records are keyed by (watched_path_id, relative_path).
// Version increments each time the file content changes and is re-backed-up.
type FileBackup struct {
	ID             int64     `json:"id"`
	UserID         int64     `json:"user_id"`
	WatchedPathID  int64     `json:"watched_path_id"`
	RelativePath   string    `json:"relative_path"`
	Size           int64     `json:"size"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	ObjectKey      string    `json:"object_key"`
	BackedUpAt     time.Time `json:"backed_up_at"`
	Version        int       `json:"version"`
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
