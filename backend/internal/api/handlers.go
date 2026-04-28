package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/models"
	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
	"github.com/ali-sab/cloudbackupserver/backend/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	db         *pgxpool.Pool // may be nil in unit tests that don't touch the DB
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
// Tokens travel exclusively via HttpOnly cookies — never in the response body.
type AuthResponse struct {
	User UserInfo `json:"user"`
}

// Cookie names used for auth tokens.
const (
	cookieAccessToken  = "access_token"
	cookieRefreshToken = "refresh_token"

	// maxJSONBody bounds the size of any JSON request body. None of our
	// JSON endpoints legitimately need more than this, and an unbounded
	// decoder is a trivial DoS vector.
	maxJSONBody = 1 << 20 // 1 MiB

	// maxRelativePathLen caps relative_path length to a sane value.
	// 1024 is generous — most filesystems cap at 255 per segment, 4096 total.
	maxRelativePathLen = 1024

	// maxBackupSize caps a single uploaded file at 10 GiB. Adjust upward if needed.
	maxBackupSize = 10 * 1024 * 1024 * 1024
)

// secureCookies controls whether Set-Cookie includes the Secure flag.
// Toggled by env var COOKIE_SECURE — disabled for local dev (HTTP), enabled
// in production (HTTPS).
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
	Files []FileEntry `json:"files"`
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
// Logs (debug-style) on decode failure so transient client bugs are diagnosable.
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

// validateRelativePath enforces a strict shape on user-supplied relative
// paths used as object-storage keys and URL components. Returns the cleaned
// path (always POSIX, no leading slash) on success.
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

// validatePassword enforces a minimum length, a maximum (bcrypt's truncation
// limit), and rejects passwords containing a NUL byte. Intentionally lenient
// on complexity so the user can test with simple passwords; the renderer
// shows a strength indicator for guidance.
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

// ---- Handlers ----

// GetHealth handles GET /api/health.
func (h *Handler) GetHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok", Version: "0.4.0"})
}

// GetSession handles GET /api/session.
// Returns current session state based on the access_token cookie.
// Always returns 200 — missing/invalid cookie yields {logged_in: false}.
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(cookieAccessToken)
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusOK, SessionResponse{LoggedIn: false})
		return
	}
	claims, err := h.sessionSvc.ValidateAccessToken(cookie.Value)
	if err != nil {
		writeJSON(w, http.StatusOK, SessionResponse{LoggedIn: false})
		return
	}
	writeJSON(w, http.StatusOK, SessionResponse{
		LoggedIn: true,
		User:     &UserInfo{ID: claims.UserID, Email: claims.Email},
	})
}

// PostLogin handles POST /api/auth/login.
func (h *Handler) PostLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "email and password are required"})
		return
	}

	user, err := db.GetUserByEmail(r.Context(), h.db, req.Email)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid credentials"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid credentials"})
		return
	}

	accessToken, rawRefresh, err := h.issueTokenPair(r, user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create session"})
		return
	}

	setAuthCookies(w, accessToken, rawRefresh)
	writeJSON(w, http.StatusOK, AuthResponse{User: UserInfo{ID: user.ID, Email: user.Email}})
}

// PostRegister handles POST /api/auth/register.
func (h *Handler) PostRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "email is required"})
		return
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "email is not a valid address"})
		return
	}
	if err := validatePassword(req.Password); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), h.bcryptCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to process password"})
		return
	}

	user := &models.User{Email: req.Email, PasswordHash: string(hash)}
	if err := db.CreateUser(r.Context(), h.db, user); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already registered"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create account"})
		}
		return
	}

	accessToken, rawRefresh, err := h.issueTokenPair(r, user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create session"})
		return
	}

	setAuthCookies(w, accessToken, rawRefresh)
	writeJSON(w, http.StatusCreated, AuthResponse{User: UserInfo{ID: user.ID, Email: user.Email}})
}

// PostRefresh handles POST /api/auth/refresh.
// Reads the refresh token from the cookie, rotates it, and sets new cookies.
func (h *Handler) PostRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(cookieRefreshToken)
	if err != nil || cookie.Value == "" {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "refresh cookie missing"})
		return
	}

	hash := session.HashToken(cookie.Value)
	rt, err := db.GetRefreshTokenByHash(r.Context(), h.db, hash)
	if err != nil {
		clearAuthCookies(w)
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid refresh token"})
		return
	}

	// Theft detection: revoked token re-presented → revoke all tokens for this user.
	if rt.Revoked {
		_ = db.RevokeAllUserRefreshTokens(r.Context(), h.db, rt.UserID)
		clearAuthCookies(w)
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "refresh token reuse detected — all sessions revoked"})
		return
	}

	if time.Now().After(rt.ExpiresAt) {
		clearAuthCookies(w)
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "refresh token expired"})
		return
	}

	if err := db.RevokeRefreshToken(r.Context(), h.db, rt.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to rotate token"})
		return
	}

	user, err := db.GetUserByID(r.Context(), h.db, rt.UserID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "user not found"})
		return
	}

	accessToken, rawRefresh, err := h.issueTokenPair(r, user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to issue new tokens"})
		return
	}

	setAuthCookies(w, accessToken, rawRefresh)
	writeJSON(w, http.StatusOK, AuthResponse{User: UserInfo{ID: user.ID, Email: user.Email}})
}

// PostLogout handles POST /api/auth/logout.
// Revokes the refresh token (from cookie). Idempotent — succeeds even if absent.
func (h *Handler) PostLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(cookieRefreshToken); err == nil && cookie.Value != "" {
		hash := session.HashToken(cookie.Value)
		rt, err := db.GetRefreshTokenByHash(r.Context(), h.db, hash)
		if err == nil && !rt.Revoked {
			_ = db.RevokeRefreshToken(r.Context(), h.db, rt.ID)
		}
	}
	clearAuthCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

// PostForgotPassword handles POST /api/auth/forgot-password.
//
// TODO(forgot-password): see ForgotPasswordResponse — flow is under development.
// Today it leaks account existence and returns reset_token in the body. Both
// must change before production.
func (h *Handler) PostForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req ForgotPasswordRequest
	if err := decodeJSON(r, &req); err != nil || req.Email == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "email is required"})
		return
	}

	user, err := db.GetUserByEmail(r.Context(), h.db, req.Email)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "no account found with that email"})
		return
	}

	rawToken, hash, err := session.GenerateRefreshToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to generate reset token"})
		return
	}

	expiresAt := time.Now().Add(session.PasswordResetTokenTTL)
	if err := db.CreatePasswordResetToken(r.Context(), h.db, user.ID, hash, expiresAt); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to store reset token"})
		return
	}

	writeJSON(w, http.StatusOK, ForgotPasswordResponse{
		Message:    "If the account exists, a reset token has been issued.",
		ResetToken: rawToken,
		DevNote:    "reset_token is returned in the response body for development only; this field will be removed when email delivery is added",
	})
}

// PostResetPassword handles POST /api/auth/reset-password.
func (h *Handler) PostResetPassword(w http.ResponseWriter, r *http.Request) {
	var req ResetPasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.ResetToken == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "reset_token is required"})
		return
	}
	if err := validatePassword(req.NewPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	hash := session.HashToken(req.ResetToken)
	prt, err := db.GetPasswordResetTokenByHash(r.Context(), h.db, hash)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid reset token"})
		return
	}
	if prt.Used {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "reset token has already been used"})
		return
	}
	if time.Now().After(prt.ExpiresAt) {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "reset token has expired"})
		return
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), h.bcryptCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to process password"})
		return
	}

	if err := db.UpdateUserPassword(r.Context(), h.db, prt.UserID, string(newHash)); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update password"})
		return
	}

	if err := db.MarkPasswordResetTokenUsed(r.Context(), h.db, prt.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to complete password reset"})
		return
	}

	_ = db.RevokeAllUserRefreshTokens(r.Context(), h.db, prt.UserID)

	writeJSON(w, http.StatusOK, map[string]string{"message": "Password updated successfully. Please log in again."})
}

// ---- Folder handlers ----

func parseFolderID(w http.ResponseWriter, r *http.Request) int64 {
	idStr := chi.URLParam(r, "folderID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid folder id"})
		return 0
	}
	return id
}

func (h *Handler) GetFolders(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	stats, err := db.GetFolderStats(r.Context(), h.db, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to load folders"})
		return
	}
	writeJSON(w, http.StatusOK, FolderStatsResponse{Folders: stats})
}

func (h *Handler) PostFolder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	var req AddFolderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "path is required"})
		return
	}

	name := req.Name
	if name == "" {
		name = filepath.Base(req.Path)
	}

	wp, err := db.AddWatchedPath(r.Context(), h.db, userID, req.Path, name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to add folder"})
		return
	}
	writeJSON(w, http.StatusCreated, FolderResponse{ID: wp.ID, Path: wp.Path, Name: wp.Name, CreatedAt: wp.CreatedAt})
}

func (h *Handler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	folderID := parseFolderID(w, r)
	if folderID == 0 {
		return
	}

	_, err := db.GetWatchedPathByID(r.Context(), h.db, folderID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "folder not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve folder"})
		}
		return
	}

	if h.storage != nil {
		backups, err := db.GetFileBackupsByWatchedPathID(r.Context(), h.db, folderID)
		if err != nil {
			log.Printf("warn: failed to list backups for folder %d during delete: %v", folderID, err)
		} else {
			for _, b := range backups {
				if delErr := h.storage.DeleteObject(r.Context(), b.ObjectKey); delErr != nil {
					log.Printf("warn: failed to delete object %q for folder %d: %v", b.ObjectKey, folderID, delErr)
				}
			}
		}
	}

	if err := db.DeleteWatchedPath(r.Context(), h.db, folderID, userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "folder not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete folder"})
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) PutFolder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	folderID := parseFolderID(w, r)
	if folderID == 0 {
		return
	}

	var req RenameFolderRequest
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "name is required"})
		return
	}

	if err := db.RenameWatchedPath(r.Context(), h.db, folderID, userID, strings.TrimSpace(req.Name)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "folder not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to rename folder"})
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) PutAccountEmail(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	var req ChangeEmailRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if strings.TrimSpace(req.NewEmail) == "" || req.CurrentPassword == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "new_email and current_password are required"})
		return
	}
	if _, err := mail.ParseAddress(req.NewEmail); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "new_email is not a valid email address"})
		return
	}

	user, err := db.GetUserByID(r.Context(), h.db, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve user"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)) != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "incorrect password"})
		return
	}

	if err := db.UpdateUserEmail(r.Context(), h.db, userID, strings.TrimSpace(req.NewEmail)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already in use"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update email"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": strings.TrimSpace(req.NewEmail)})
}

func (h *Handler) PutAccountPassword(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	var req ChangePasswordRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.CurrentPassword == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "current_password is required"})
		return
	}
	if err := validatePassword(req.NewPassword); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	user, err := db.GetUserByID(r.Context(), h.db, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve user"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)) != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "incorrect current password"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), h.bcryptCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to hash password"})
		return
	}
	if err := db.UpdateUserPassword(r.Context(), h.db, userID, string(hash)); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update password"})
		return
	}

	if err := db.RevokeAllUserRefreshTokens(r.Context(), h.db, userID); err != nil {
		log.Printf("warn: failed to revoke tokens after password change for user %d: %v", userID, err)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	var req DeleteAccountRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.CurrentPassword == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "current_password is required"})
		return
	}

	user, err := db.GetUserByID(r.Context(), h.db, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve user"})
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)) != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "incorrect password"})
		return
	}

	if h.storage != nil {
		if delErr := h.storage.DeleteUserObjects(r.Context(), userID); delErr != nil {
			log.Printf("warn: failed to delete storage objects for user %d during account deletion: %v", userID, delErr)
		}
	}

	if err := db.DeleteUser(r.Context(), h.db, userID); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to delete account"})
		return
	}

	clearAuthCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) requireFolder(w http.ResponseWriter, r *http.Request, userID int64) *models.WatchedPath {
	folderID := parseFolderID(w, r)
	if folderID == 0 {
		return nil
	}
	wp, err := db.GetWatchedPathByID(r.Context(), h.db, folderID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "folder not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve folder"})
		}
		return nil
	}
	return wp
}

func (h *Handler) GetFiles(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}
	files, err := db.GetWatchedFiles(r.Context(), h.db, wp.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve files"})
		return
	}
	writeJSON(w, http.StatusOK, WatchedFilesResponse{Files: files})
}

func (h *Handler) PutSyncFiles(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}

	// Sync payloads can be large for big trees — bump the cap.
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20) // 32 MiB
	var req SyncWatchedFilesRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		log.Printf("debug: sync decode failed: %v", err)
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Files == nil {
		req.Files = []FileEntry{}
	}

	watchedFiles := make([]models.WatchedFile, len(req.Files))
	for i, f := range req.Files {
		cleaned, pathErr := validateRelativePath(f.RelativePath)
		if pathErr != nil {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: fmt.Sprintf("files[%d].relative_path: %s", i, pathErr.Error())})
			return
		}
		watchedFiles[i] = models.WatchedFile{
			Name:         f.Name,
			RelativePath: cleaned,
			IsDirectory:  f.IsDirectory,
			Size:         f.Size,
			ModifiedMs:   f.ModifiedMs,
		}
	}

	if err := db.SyncWatchedFiles(r.Context(), h.db, wp.ID, watchedFiles); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to sync files"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Backup handlers ----

type HistoryResponse struct {
	Items  []db.HistoryItem `json:"items"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

func (h *Handler) GetBackupHistory(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	folderID, _ := strconv.ParseInt(r.URL.Query().Get("folder_id"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	items, err := db.GetBackupHistory(r.Context(), h.db, userID, folderID, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to load history"})
		return
	}
	writeJSON(w, http.StatusOK, HistoryResponse{Items: items, Limit: limit, Offset: offset})
}

type FileVersionsResponse struct {
	Versions []db.FileVersion `json:"versions"`
}

func (h *Handler) GetFileVersions(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}
	relativePath := r.URL.Query().Get("path")
	if relativePath == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "path query parameter is required"})
		return
	}
	relativePath, err := validateRelativePath(relativePath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}
	versions, err := db.GetFileVersions(r.Context(), h.db, wp.ID, relativePath)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to load versions"})
		return
	}
	writeJSON(w, http.StatusOK, FileVersionsResponse{Versions: versions})
}

func (h *Handler) GetFileVersionDownload(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}
	versionID, err := strconv.ParseInt(chi.URLParam(r, "versionID"), 10, 64)
	if err != nil || versionID <= 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid versionID"})
		return
	}
	v, err := db.GetFileVersionByID(r.Context(), h.db, versionID, userID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "version not found"})
		return
	}
	if v.WatchedPathID != wp.ID {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "version not found"})
		return
	}
	obj, _, err := h.storage.GetObject(r.Context(), v.ObjectKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve object"})
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Backup-Version", strconv.Itoa(v.Version))
	w.Header().Set("X-Checksum-SHA256", v.ChecksumSHA256)
	w.Header().Set("Content-Length", strconv.FormatInt(v.Size, 10))
	if _, err := io.Copy(w, obj); err != nil {
		log.Printf("warn: short copy on version download key=%q err=%v", v.ObjectKey, err)
	}
}

func (h *Handler) GetFileBackups(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}
	backups, err := db.GetFileBackupsByWatchedPathID(r.Context(), h.db, wp.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to load backups"})
		return
	}
	writeJSON(w, http.StatusOK, FileBackupsResponse{Backups: backups})
}

// PutFileBackup handles PUT /api/folders/{folderID}/backup/*.
// Streams the request body into object storage. Skips when checksum matches.
func (h *Handler) PutFileBackup(w http.ResponseWriter, r *http.Request) {
	relativePath, err := validateRelativePath(chi.URLParam(r, "*"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	checksum := strings.ToLower(r.Header.Get("X-Checksum-SHA256"))
	if checksum == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "X-Checksum-SHA256 header is required"})
		return
	}
	if len(checksum) != 64 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "X-Checksum-SHA256 must be a 64-character hex string"})
		return
	}
	for _, c := range checksum {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "X-Checksum-SHA256 must be a 64-character hex string"})
			return
		}
	}

	fileSizeStr := r.Header.Get("X-File-Size")
	fileSize, fileSizeErr := strconv.ParseInt(fileSizeStr, 10, 64)
	if fileSizeErr != nil || fileSize < 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "X-File-Size header is required and must be a non-negative integer"})
		return
	}
	if fileSize > maxBackupSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, ErrorResponse{Error: "file exceeds maximum allowed size"})
		return
	}

	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}

	existing, lookupErr := db.GetFileBackup(r.Context(), h.db, wp.ID, relativePath)
	if lookupErr == nil && existing.ChecksumSHA256 == checksum {
		writeJSON(w, http.StatusOK, UploadFileResponse{
			RelativePath:   existing.RelativePath,
			Size:           existing.Size,
			ChecksumSHA256: existing.ChecksumSHA256,
			BackedUpAt:     existing.BackedUpAt,
			Version:        existing.Version,
			Skipped:        true,
		})
		return
	}

	objectKey, err := storage.ObjectKey(userID, wp.ID, relativePath)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	// If a prior backup exists with a different key (e.g. legacy 2-part format),
	// queue its old object for deletion after the new write succeeds.
	var oldKey string
	if lookupErr == nil && existing.ObjectKey != objectKey {
		oldKey = existing.ObjectKey
	}

	// Cap the body at the declared size + a small slack to defend against malicious
	// content-length lies. MinIO uses size to drive multipart and short-reads error.
	r.Body = http.MaxBytesReader(w, r.Body, fileSize+1024)

	if err := h.storage.PutObject(r.Context(), objectKey, r.Body, fileSize, "application/octet-stream"); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "upload failed"})
		return
	}

	if oldKey != "" {
		if delErr := h.storage.DeleteObject(r.Context(), oldKey); delErr != nil {
			log.Printf("warn: failed to delete old object %q after re-upload: %v", oldKey, delErr)
		}
	}

	backup := &models.FileBackup{
		UserID:         userID,
		WatchedPathID:  wp.ID,
		RelativePath:   relativePath,
		Size:           fileSize,
		ChecksumSHA256: checksum,
		ObjectKey:      objectKey,
	}
	if err := db.UpsertFileBackup(r.Context(), h.db, backup); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to record backup"})
		return
	}

	writeJSON(w, http.StatusOK, UploadFileResponse{
		RelativePath:   backup.RelativePath,
		Size:           backup.Size,
		ChecksumSHA256: backup.ChecksumSHA256,
		BackedUpAt:     backup.BackedUpAt,
		Version:        backup.Version,
		Skipped:        false,
	})
}

func (h *Handler) GetFileBackup(w http.ResponseWriter, r *http.Request) {
	relativePath, err := validateRelativePath(chi.URLParam(r, "*"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: err.Error()})
		return
	}

	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}

	backup, err := db.GetFileBackup(r.Context(), h.db, wp.ID, relativePath)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "file not backed up"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve backup record"})
		}
		return
	}

	reader, size, err := h.storage.GetObject(r.Context(), backup.ObjectKey)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve file"})
		return
	}
	defer reader.Close()

	fileName := filepath.Base(relativePath)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("warn: short copy on backup download key=%q err=%v", backup.ObjectKey, err)
	}
}

// ---- Helpers ----

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
// Secure is set in production (env COOKIE_SECURE=true). Path=/ on both so
// /api/auth/logout can clear them without path acrobatics.
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
