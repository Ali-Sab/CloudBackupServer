package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/models"
	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
	"github.com/ali-sab/cloudbackupserver/backend/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Version is injected at build time via -ldflags "-X ...api.Version=x.y.z".
// Falls back to "dev" when built without ldflags (local go run).
var Version = "dev"

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	db         *pgxpool.Pool   // may be nil in unit tests that don't touch the DB
	sessionSvc *session.Service
	storage    storage.Backend // may be nil in unit tests that don't touch storage
	bcryptCost int
}

// NewHandler creates a Handler with the provided dependencies.
func NewHandler(pool *pgxpool.Pool, sessionSvc *session.Service, store storage.Backend) *Handler {
	return &Handler{db: pool, sessionSvc: sessionSvc, storage: store, bcryptCost: bcrypt.DefaultCost}
}

// newTestHandler creates a Handler with bcrypt.MinCost for faster tests.
func newTestHandler(pool *pgxpool.Pool, sessionSvc *session.Service, store storage.Backend) *Handler {
	return &Handler{db: pool, sessionSvc: sessionSvc, storage: store, bcryptCost: bcrypt.MinCost}
}

// ---- Request / response types (exported for tests) ----

// HealthResponse is returned by GET /api/health.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// SessionResponse is returned by GET /api/session.
type SessionResponse struct {
	LoggedIn bool      `json:"logged_in"`
	User     *UserInfo `json:"user,omitempty"`
}

// UserInfo is the public-facing subset of a user record.
type UserInfo struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
}

// AuthResponse is returned after a successful login or registration.
type AuthResponse struct {
	User UserInfo `json:"user"`
}

// Cookie names used for auth tokens.
const (
	cookieAccessToken  = "access_token"
	cookieRefreshToken = "refresh_token"

	maxJSONBody        = 1 << 20              // 1 MiB
	maxRelativePathLen = 1024
	maxBackupSize      = 10 * 1024 * 1024 * 1024 // 10 GiB
)

// secureCookies controls whether Set-Cookie includes the Secure flag.
var secureCookies = os.Getenv("COOKIE_SECURE") == "true"

// SetSecureCookies allows main / tests to override the env-derived default.
func SetSecureCookies(v bool) { secureCookies = v }

// ErrorResponse wraps an error message returned to the client.
type ErrorResponse struct {
	Error string `json:"error"`
}

// LoginRequest is the body expected by POST /api/auth/login.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// RegisterRequest is the body expected by POST /api/auth/register.
type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// ForgotPasswordRequest is the body expected by POST /api/auth/forgot-password.
type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

// ForgotPasswordResponse is returned by POST /api/auth/forgot-password.
//
// TODO(forgot-password): the entire forgot/reset flow is under development.
// Today it leaks account existence (404 when email unknown) and returns the
// reset token in the response body. Before shipping, switch to an always-200
// generic response, deliver the token via email, and remove ResetToken/DevNote
// fields. Tracked separately — left as-is for now per product decision.
type ForgotPasswordResponse struct {
	Message    string `json:"message"`
	ResetToken string `json:"reset_token,omitempty"` // DEV ONLY — see TODO above
	DevNote    string `json:"_dev_note,omitempty"`
}

// ResetPasswordRequest is the body expected by POST /api/auth/reset-password.
type ResetPasswordRequest struct {
	ResetToken  string `json:"reset_token"`
	NewPassword string `json:"new_password"`
}

// AddFolderRequest is the body expected by POST /api/folders.
type AddFolderRequest struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

// RenameFolderRequest is the body expected by PUT /api/folders/{id}.
type RenameFolderRequest struct {
	Name string `json:"name"`
}

// ChangeEmailRequest is the body expected by PUT /api/account/email.
type ChangeEmailRequest struct {
	NewEmail        string `json:"new_email"`
	CurrentPassword string `json:"current_password"`
}

// ChangePasswordRequest is the body expected by PUT /api/account/password.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// DeleteAccountRequest is the body expected by DELETE /api/account.
type DeleteAccountRequest struct {
	CurrentPassword string `json:"current_password"`
}

// FolderStatsResponse is returned by GET /api/folders.
type FolderStatsResponse struct {
	Folders []models.FolderStats `json:"folders"`
}

// FolderResponse is returned by POST /api/folders.
type FolderResponse struct {
	ID        int64     `json:"id"`
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// FileEntry describes a single file or directory sent by the client.
type FileEntry struct {
	Name         string `json:"name"`
	RelativePath string `json:"relative_path"`
	IsDirectory  bool   `json:"is_directory"`
	Size         int64  `json:"size"`
	ModifiedMs   int64  `json:"modified_ms"`
}

// SyncWatchedFilesRequest is the body expected by PUT /api/folders/{id}/sync.
type SyncWatchedFilesRequest struct {
	Files         []FileEntry `json:"files"`
	PruneDeleted  bool        `json:"prune_deleted"`
}

// WatchedFilesResponse is returned by GET /api/folders/{id}/files.
type WatchedFilesResponse struct {
	Files []models.WatchedFile `json:"files"`
}

// FileBackupsResponse is returned by GET /api/folders/{id}/backups.
type FileBackupsResponse struct {
	Backups []models.FileBackup `json:"backups"`
}

// UploadFileResponse is returned by PUT /api/folders/{id}/backup/*.
type UploadFileResponse struct {
	RelativePath   string    `json:"relative_path"`
	Size           int64     `json:"size"`
	ChecksumSHA256 string    `json:"checksum_sha256"`
	BackedUpAt     time.Time `json:"backed_up_at"`
	Version        int       `json:"version"`
	Skipped        bool      `json:"skipped"`
}

// ---- Helpers ----

// decodeJSON wraps r.Body with a size cap and decodes into v.
func decodeJSON(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, maxJSONBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		log.Printf("debug: json decode failed for %s %s: %v", r.Method, r.URL.Path, err)
		return err
	}
	return nil
}

// validateRelativePath enforces a strict shape on user-supplied relative paths.
func validateRelativePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("relative_path is required")
	}
	if len(p) > maxRelativePathLen {
		return "", errors.New("relative_path is too long")
	}
	if strings.ContainsRune(p, 0) {
		return "", errors.New("relative_path contains NUL byte")
	}
	if strings.ContainsAny(p, "\\") {
		return "", errors.New("relative_path must use POSIX separators")
	}
	if strings.HasPrefix(p, "/") {
		return "", errors.New("relative_path must be relative")
	}
	cleaned := path.Clean(p)
	if cleaned != p {
		return "", errors.New("relative_path must be in canonical form")
	}
	for _, seg := range strings.Split(cleaned, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("invalid path segment")
		}
	}
	return cleaned, nil
}

// validatePassword enforces minimum length, bcrypt max, and no NUL bytes.
func validatePassword(p string) error {
	if len(p) < session.MinPasswordLength {
		return fmt.Errorf("password must be at least %d characters", session.MinPasswordLength)
	}
	if len(p) > session.MaxPasswordLength {
		return fmt.Errorf("password must be no more than %d characters", session.MaxPasswordLength)
	}
	if strings.ContainsRune(p, 0) {
		return errors.New("password contains NUL byte")
	}
	return nil
}
// issueTokenPair creates a new access token and refresh token for the given user,
// persists the refresh token hash to the database, and returns both raw tokens.
func (h *Handler) issueTokenPair(r *http.Request, user *models.User) (accessToken, rawRefresh string, err error) {
	accessToken, err = h.sessionSvc.CreateAccessToken(user.ID, user.Email)
	if err != nil {
		return "", "", err
	}

	rawRefresh, hash, err := session.GenerateRefreshToken()
	if err != nil {
		return "", "", err
	}

	expiresAt := time.Now().Add(session.RefreshTokenTTL)
	if err := db.CreateRefreshToken(r.Context(), h.db, user.ID, hash, expiresAt); err != nil {
		return "", "", err
	}

	return accessToken, rawRefresh, nil
}

// setAuthCookies installs both cookies with SameSite=Strict + HttpOnly.
// Cookies are always persistent — Electron keeps the session alive by default.
func setAuthCookies(w http.ResponseWriter, accessToken, rawRefresh string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieAccessToken,
		Value:    accessToken,
		Path:     "/",
		MaxAge:   int(session.AccessTokenTTL.Seconds()),
		HttpOnly: true,
		Secure:   secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cookieRefreshToken,
		Value:    rawRefresh,
		Path:     "/",
		MaxAge:   int(session.RefreshTokenTTL.Seconds()),
		HttpOnly: true,
		Secure:   secureCookies,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearAuthCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieAccessToken, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secureCookies, SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: cookieRefreshToken, Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// sanitizeHeaderFilename strips characters that could break a Content-Disposition header value.
func sanitizeHeaderFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '"' || r == '\\' || r < 0x20 {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
