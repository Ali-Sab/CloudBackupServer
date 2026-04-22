package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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

// UpdateUserEmail changes the email address for the given user.
// Returns a pgconn.PgError with Code "23505" if the new email is already taken.
func UpdateUserEmail(ctx context.Context, pool *pgxpool.Pool, userID int64, newEmail string) error {
	_, err := pool.Exec(ctx,
		`UPDATE users SET email = $1, updated_at = NOW() WHERE id = $2`,
		newEmail, userID,
	)
	if err != nil {
		return fmt.Errorf("updating user email: %w", err)
	}
	return nil
}

// DeleteUser permanently removes the user row. All child rows (refresh_tokens,
// password_reset_tokens, watched_paths, watched_files, file_backups) are
// cascade-deleted by the database FK constraints.
func DeleteUser(ctx context.Context, pool *pgxpool.Pool, userID int64) error {
	_, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("deleting user: %w", err)
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

// ---- Watched paths / folders ----

// AddWatchedPath inserts a new watched directory for the user.
// Multiple paths per user are allowed.
func AddWatchedPath(ctx context.Context, pool *pgxpool.Pool, userID int64, path, name string) (*models.WatchedPath, error) {
	wp := &models.WatchedPath{}
	err := pool.QueryRow(ctx,
		`INSERT INTO watched_paths (user_id, path, name)
		 VALUES ($1, $2, $3)
		 RETURNING id, user_id, path, name, created_at, updated_at`,
		userID, path, name,
	).Scan(&wp.ID, &wp.UserID, &wp.Path, &wp.Name, &wp.CreatedAt, &wp.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("adding watched path: %w", err)
	}
	return wp, nil
}

// GetWatchedPathsByUserID returns all watched paths for a user, ordered by created_at ASC.
func GetWatchedPathsByUserID(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]models.WatchedPath, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, user_id, path, name, created_at, updated_at
		 FROM watched_paths WHERE user_id = $1
		 ORDER BY created_at ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying watched paths: %w", err)
	}
	defer rows.Close()

	var paths []models.WatchedPath
	for rows.Next() {
		var wp models.WatchedPath
		if err := rows.Scan(&wp.ID, &wp.UserID, &wp.Path, &wp.Name, &wp.CreatedAt, &wp.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning watched path: %w", err)
		}
		paths = append(paths, wp)
	}
	if paths == nil {
		paths = []models.WatchedPath{}
	}
	return paths, rows.Err()
}

// GetWatchedPathByID returns a watched path by id, verifying it belongs to userID.
// Returns pgx.ErrNoRows (wrapped) if not found or not owned by this user.
func GetWatchedPathByID(ctx context.Context, pool *pgxpool.Pool, id, userID int64) (*models.WatchedPath, error) {
	wp := &models.WatchedPath{}
	err := pool.QueryRow(ctx,
		`SELECT id, user_id, path, name, created_at, updated_at
		 FROM watched_paths WHERE id = $1 AND user_id = $2`,
		id, userID,
	).Scan(&wp.ID, &wp.UserID, &wp.Path, &wp.Name, &wp.CreatedAt, &wp.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("getting watched path by id: %w", err)
	}
	return wp, nil
}

// DeleteWatchedPath removes a watched path (and its watched_files and file_backups via CASCADE).
// Returns pgx.ErrNoRows (wrapped) if not found or not owned by this user.
func DeleteWatchedPath(ctx context.Context, pool *pgxpool.Pool, id, userID int64) error {
	tag, err := pool.Exec(ctx,
		`DELETE FROM watched_paths WHERE id = $1 AND user_id = $2`,
		id, userID,
	)
	if err != nil {
		return fmt.Errorf("deleting watched path: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("watched path %d not found: %w", id, pgx.ErrNoRows)
	}
	return nil
}

// RenameWatchedPath updates the display name of a watched path owned by userID.
// Returns pgx.ErrNoRows (wrapped) if not found or not owned by this user.
func RenameWatchedPath(ctx context.Context, pool *pgxpool.Pool, id, userID int64, name string) error {
	tag, err := pool.Exec(ctx,
		`UPDATE watched_paths SET name = $1, updated_at = NOW() WHERE id = $2 AND user_id = $3`,
		name, id, userID,
	)
	if err != nil {
		return fmt.Errorf("renaming watched path: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("watched path %d not found: %w", id, pgx.ErrNoRows)
	}
	return nil
}

// GetFolderStats returns dashboard aggregate rows for all of a user's watched paths.
// Results are sorted by last_backed_up_at DESC NULLS LAST, then path ASC.
func GetFolderStats(ctx context.Context, pool *pgxpool.Pool, userID int64) ([]models.FolderStats, error) {
	rows, err := pool.Query(ctx,
		`SELECT wp.id, wp.path, wp.name,
		        COUNT(fb.id)               AS file_count,
		        COALESCE(SUM(fb.size), 0)  AS total_size_bytes,
		        MAX(fb.backed_up_at)       AS last_backed_up_at
		 FROM   watched_paths wp
		 LEFT JOIN file_backups fb ON fb.watched_path_id = wp.id
		 WHERE  wp.user_id = $1
		 GROUP  BY wp.id, wp.path, wp.name
		 ORDER  BY last_backed_up_at DESC NULLS LAST, wp.path ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying folder stats: %w", err)
	}
	defer rows.Close()

	var stats []models.FolderStats
	for rows.Next() {
		var s models.FolderStats
		if err := rows.Scan(&s.ID, &s.Path, &s.Name, &s.FileCount, &s.TotalSizeBytes, &s.LastBackedUpAt); err != nil {
			return nil, fmt.Errorf("scanning folder stats: %w", err)
		}
		stats = append(stats, s)
	}
	if stats == nil {
		stats = []models.FolderStats{}
	}
	return stats, rows.Err()
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

// ---- File backups ----

// UpsertFileBackup inserts or updates the current backup record for a file, then
// appends an immutable row to file_backup_versions so every version is preserved
// and restorable. Both writes run in a single transaction.
func UpsertFileBackup(ctx context.Context, pool *pgxpool.Pool, b *models.FileBackup) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("beginning upsert transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	err = tx.QueryRow(ctx,
		`INSERT INTO file_backups (user_id, watched_path_id, relative_path, size, checksum_sha256, object_key)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (watched_path_id, relative_path) DO UPDATE
		   SET size            = EXCLUDED.size,
		       checksum_sha256 = EXCLUDED.checksum_sha256,
		       object_key      = EXCLUDED.object_key,
		       backed_up_at    = NOW(),
		       version         = file_backups.version + 1
		 RETURNING id, backed_up_at, version`,
		b.UserID, b.WatchedPathID, b.RelativePath, b.Size, b.ChecksumSHA256, b.ObjectKey,
	).Scan(&b.ID, &b.BackedUpAt, &b.Version)
	if err != nil {
		return fmt.Errorf("upserting file backup: %w", err)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO file_backup_versions
		    (user_id, watched_path_id, relative_path, version, size, checksum_sha256, object_key, backed_up_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (watched_path_id, relative_path, version) DO NOTHING`,
		b.UserID, b.WatchedPathID, b.RelativePath, b.Version,
		b.Size, b.ChecksumSHA256, b.ObjectKey, b.BackedUpAt,
	)
	if err != nil {
		return fmt.Errorf("inserting file backup version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("committing upsert transaction: %w", err)
	}
	return nil
}

// FileVersion is a single restorable version of a backed-up file.
type FileVersion struct {
	ID             int64     `json:"id"`
	WatchedPathID  int64     `json:"-"`
	Version        int       `json:"version"`
	Size           int64     `json:"size"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	ObjectKey      string    `json:"object_key"`
	BackedUpAt     time.Time `json:"backed_up_at"`
}

// GetFileVersions returns all preserved versions for a file, newest first.
func GetFileVersions(ctx context.Context, pool *pgxpool.Pool, watchedPathID int64, relativePath string) ([]FileVersion, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, version, size, checksum_sha256, object_key, backed_up_at
		 FROM file_backup_versions
		 WHERE watched_path_id = $1 AND relative_path = $2
		 ORDER BY version DESC`,
		watchedPathID, relativePath,
	)
	if err != nil {
		return nil, fmt.Errorf("querying file versions: %w", err)
	}
	defer rows.Close()

	var versions []FileVersion
	for rows.Next() {
		var v FileVersion
		if err := rows.Scan(&v.ID, &v.Version, &v.Size, &v.ChecksumSHA256, &v.ObjectKey, &v.BackedUpAt); err != nil {
			return nil, fmt.Errorf("scanning file version: %w", err)
		}
		versions = append(versions, v)
	}
	if versions == nil {
		versions = []FileVersion{}
	}
	return versions, rows.Err()
}

// GetFileVersionByID returns a single version row by its primary key, scoped to userID.
func GetFileVersionByID(ctx context.Context, pool *pgxpool.Pool, versionID, userID int64) (*FileVersion, error) {
	v := &FileVersion{}
	err := pool.QueryRow(ctx,
		`SELECT id, watched_path_id, version, size, checksum_sha256, object_key, backed_up_at
		 FROM file_backup_versions
		 WHERE id = $1 AND user_id = $2`,
		versionID, userID,
	).Scan(&v.ID, &v.WatchedPathID, &v.Version, &v.Size, &v.ChecksumSHA256, &v.ObjectKey, &v.BackedUpAt)
	if err != nil {
		return nil, fmt.Errorf("getting file version: %w", err)
	}
	return v, nil
}

// GetFileBackup returns the backup record for a specific watched path + relative path.
// Returns pgx.ErrNoRows (wrapped) if no backup exists.
func GetFileBackup(ctx context.Context, pool *pgxpool.Pool, watchedPathID int64, relativePath string) (*models.FileBackup, error) {
	b := &models.FileBackup{}
	err := pool.QueryRow(ctx,
		`SELECT id, user_id, watched_path_id, relative_path, size, checksum_sha256, object_key, backed_up_at, version
		 FROM file_backups WHERE watched_path_id = $1 AND relative_path = $2`,
		watchedPathID, relativePath,
	).Scan(&b.ID, &b.UserID, &b.WatchedPathID, &b.RelativePath, &b.Size, &b.ChecksumSHA256, &b.ObjectKey, &b.BackedUpAt, &b.Version)
	if err != nil {
		return nil, fmt.Errorf("getting file backup: %w", err)
	}
	return b, nil
}

// HistoryItem is a backup record enriched with its parent folder name and path,
// returned by GetBackupHistory for the activity log screen.
type HistoryItem struct {
	models.FileBackup
	FolderName string `json:"folder_name"`
	FolderPath string `json:"folder_path"`
}

// GetBackupHistory returns up to `limit` version events for `userID` sorted by
// backed_up_at DESC, enriched with the parent folder name and path.
// Queries file_backup_versions so every re-backup appears as a separate event.
// Pass folderID = 0 to query across all folders.
func GetBackupHistory(ctx context.Context, pool *pgxpool.Pool, userID, folderID int64, limit, offset int) ([]HistoryItem, error) {
	var rows pgx.Rows
	var err error
	if folderID > 0 {
		rows, err = pool.Query(ctx,
			`SELECT fv.id, fv.user_id, fv.watched_path_id, fv.relative_path,
			        fv.size, fv.checksum_sha256, fv.object_key, fv.backed_up_at, fv.version,
			        wp.name, wp.path
			 FROM file_backup_versions fv
			 JOIN watched_paths wp ON wp.id = fv.watched_path_id
			 WHERE fv.user_id = $1 AND fv.watched_path_id = $2
			 ORDER BY fv.backed_up_at DESC
			 LIMIT $3 OFFSET $4`,
			userID, folderID, limit, offset,
		)
	} else {
		rows, err = pool.Query(ctx,
			`SELECT fv.id, fv.user_id, fv.watched_path_id, fv.relative_path,
			        fv.size, fv.checksum_sha256, fv.object_key, fv.backed_up_at, fv.version,
			        wp.name, wp.path
			 FROM file_backup_versions fv
			 JOIN watched_paths wp ON wp.id = fv.watched_path_id
			 WHERE fv.user_id = $1
			 ORDER BY fv.backed_up_at DESC
			 LIMIT $2 OFFSET $3`,
			userID, limit, offset,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("querying backup history: %w", err)
	}
	defer rows.Close()

	var items []HistoryItem
	for rows.Next() {
		var it HistoryItem
		if err := rows.Scan(
			&it.ID, &it.UserID, &it.WatchedPathID, &it.RelativePath,
			&it.Size, &it.ChecksumSHA256, &it.ObjectKey, &it.BackedUpAt, &it.Version,
			&it.FolderName, &it.FolderPath,
		); err != nil {
			return nil, fmt.Errorf("scanning history item: %w", err)
		}
		items = append(items, it)
	}
	if items == nil {
		items = []HistoryItem{}
	}
	return items, rows.Err()
}

// GetFileBackupsByWatchedPathID returns all backup records for a watched path, ordered by relative_path.
func GetFileBackupsByWatchedPathID(ctx context.Context, pool *pgxpool.Pool, watchedPathID int64) ([]models.FileBackup, error) {
	rows, err := pool.Query(ctx,
		`SELECT id, user_id, watched_path_id, relative_path, size, checksum_sha256, object_key, backed_up_at, version
		 FROM file_backups WHERE watched_path_id = $1 ORDER BY relative_path ASC`,
		watchedPathID,
	)
	if err != nil {
		return nil, fmt.Errorf("querying file backups: %w", err)
	}
	defer rows.Close()

	var backups []models.FileBackup
	for rows.Next() {
		var b models.FileBackup
		if err := rows.Scan(&b.ID, &b.UserID, &b.WatchedPathID, &b.RelativePath, &b.Size, &b.ChecksumSHA256, &b.ObjectKey, &b.BackedUpAt, &b.Version); err != nil {
			return nil, fmt.Errorf("scanning file backup: %w", err)
		}
		backups = append(backups, b)
	}
	if backups == nil {
		backups = []models.FileBackup{}
	}
	return backups, rows.Err()
}
