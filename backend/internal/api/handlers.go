package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/go-chi/chi/v5"
	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/models"
	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
	"github.com/ali-sab/cloudbackupserver/backend/internal/storage"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	db           *pgxpool.Pool   // may be nil in unit tests that don't touch the DB
	sessionSvc   *session.Service
	storage      storage.Backend // may be nil in unit tests that don't touch storage
	bcryptCost  int
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
// Tokens are also set as HttpOnly cookies for browser clients.
type AuthResponse struct {
	User         UserInfo `json:"user"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
}

// RefreshResponse is returned by POST /api/auth/refresh.
// Tokens are also rotated via HttpOnly cookies for browser clients.
type RefreshResponse struct {
	User         UserInfo `json:"user"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
}

// Cookie names used for auth tokens.
const (
	cookieAccessToken  = "access_token"
	cookieRefreshToken = "refresh_token"
)

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
type ForgotPasswordResponse struct {
	Message    string `json:"message"`
	ResetToken string `json:"reset_token,omitempty"` // DEV ONLY — will move to email
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
// RelativePath is the POSIX path relative to the watched root (e.g. "photos/2024/img.jpg").
// Top-level entries have RelativePath equal to Name.
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
	Version        int       `json:"version"` // increments on each content change
	Skipped        bool      `json:"skipped"` // true when checksum matched — no upload occurred
}

// ---- Handlers ----

// GetHealth handles GET /api/health.
func (h *Handler) GetHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok", Version: "0.3.0"})
}

// GetSession handles GET /api/session.
// Returns current session state based on the Bearer token or access_token cookie.
// Always returns 200 — missing/invalid tokens yield {logged_in: false}.
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	tokenStr := extractBearerToken(r)
	if tokenStr == "" {
		if cookie, err := r.Cookie(cookieAccessToken); err == nil {
			tokenStr = cookie.Value
		}
	}
	if tokenStr == "" {
		writeJSON(w, http.StatusOK, SessionResponse{LoggedIn: false})
		return
	}

	claims, err := h.sessionSvc.ValidateAccessToken(tokenStr)
	if err != nil {
		writeJSON(w, http.StatusOK, SessionResponse{LoggedIn: false})
		return
	}

	writeJSON(w, http.StatusOK, SessionResponse{
		LoggedIn: true,
		User: &UserInfo{
			ID:    claims.UserID,
			Email: claims.Email,
		},
	})
}

// PostLogin handles POST /api/auth/login.
func (h *Handler) PostLogin(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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
	writeJSON(w, http.StatusOK, AuthResponse{
		User:         UserInfo{ID: user.ID, Email: user.Email},
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
	})
}

// PostRegister handles POST /api/auth/register.
func (h *Handler) PostRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "email and password are required"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), h.bcryptCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to process password"})
		return
	}

	user := &models.User{
		Email:        req.Email,
		PasswordHash: string(hash),
	}
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
	writeJSON(w, http.StatusCreated, AuthResponse{
		User:         UserInfo{ID: user.ID, Email: user.Email},
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
	})
}

// PostRefresh handles POST /api/auth/refresh.
// Validates the refresh token (from JSON body or cookie), rotates it (revoke old, issue new pair).
func (h *Handler) PostRefresh(w http.ResponseWriter, r *http.Request) {
	// Try JSON body first (Electron/Bearer clients send { "refresh_token": "..." }).
	var rawRefreshToken string
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
		rawRefreshToken = body.RefreshToken
	}
	// Fall back to cookie (browser clients and Insomnia cookie jar).
	if rawRefreshToken == "" {
		if cookie, err := r.Cookie(cookieRefreshToken); err == nil {
			rawRefreshToken = cookie.Value
		}
	}
	if rawRefreshToken == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "refresh_token is required"})
		return
	}

	hash := session.HashToken(rawRefreshToken)
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

	// Rotate: revoke the old token before issuing a new pair.
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
	writeJSON(w, http.StatusOK, RefreshResponse{
		User:         UserInfo{ID: user.ID, Email: user.Email},
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
	})
}

// PostLogout handles POST /api/auth/logout.
// Revokes the refresh token (from JSON body or cookie). Idempotent — succeeds even if absent or already revoked.
func (h *Handler) PostLogout(w http.ResponseWriter, r *http.Request) {
	var rawToken string
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
		rawToken = body.RefreshToken
	}
	if rawToken == "" {
		if cookie, err := r.Cookie(cookieRefreshToken); err == nil {
			rawToken = cookie.Value
		}
	}

	if rawToken != "" {
		hash := session.HashToken(rawToken)
		rt, err := db.GetRefreshTokenByHash(r.Context(), h.db, hash)
		if err == nil && !rt.Revoked {
			// Best-effort revocation — ignore error so logout is always idempotent.
			_ = db.RevokeRefreshToken(r.Context(), h.db, rt.ID)
		}
	}

	clearAuthCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

// PostForgotPassword handles POST /api/auth/forgot-password.
// Issues a password-reset token. Response always looks the same to prevent email enumeration.
// NOTE: reset_token is returned in the response body for development purposes only.
//       In production this token would be sent via email and omitted from the response.
func (h *Handler) PostForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req ForgotPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "email is required"})
		return
	}

	// DEV MODE: return a clear 404 when the email isn't found so it's easy to spot
	// missing accounts during development. Before shipping, replace this with a
	// generic 200 {"message": genericMsg} response to prevent email enumeration.
	user, err := db.GetUserByEmail(r.Context(), h.db, req.Email)
	if err != nil {
		writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "no account found with that email"})
		return
	}

	genericMsg := "If the account exists, a reset token has been issued."

	rawToken, hash, err := session.GenerateRefreshToken() // reuse the same generator
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
		Message:    genericMsg,
		ResetToken: rawToken,
		DevNote:    "reset_token is returned in the response body for development only; this field will be removed when email delivery is added",
	})
}

// PostResetPassword handles POST /api/auth/reset-password.
// Validates the reset token, updates the password, and revokes all sessions.
func (h *Handler) PostResetPassword(w http.ResponseWriter, r *http.Request) {
	var req ResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.ResetToken == "" || req.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "reset_token and new_password are required"})
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

	// Consume the reset token so it cannot be replayed.
	// This must succeed — if it fails, return an error rather than letting the
	// token remain reusable.
	if err := db.MarkPasswordResetTokenUsed(r.Context(), h.db, prt.ID); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to complete password reset"})
		return
	}

	// Revoke all active sessions — user must log in again with the new password.
	// Best-effort: sessions are invalidated naturally when the access token expires.
	_ = db.RevokeAllUserRefreshTokens(r.Context(), h.db, prt.UserID)

	writeJSON(w, http.StatusOK, map[string]string{"message": "Password updated successfully. Please log in again."})
}

// ---- Folder handlers ----

// parseFolderID extracts and validates the {folderID} URL param. Returns 0 on error (response already written).
func parseFolderID(w http.ResponseWriter, r *http.Request) int64 {
	idStr := chi.URLParam(r, "folderID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid folder id"})
		return 0
	}
	return id
}

// GetFolders handles GET /api/folders.
// Returns all of the authenticated user's watched folders with aggregate stats.
func (h *Handler) GetFolders(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	stats, err := db.GetFolderStats(r.Context(), h.db, userID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to load folders"})
		return
	}
	writeJSON(w, http.StatusOK, FolderStatsResponse{Folders: stats})
}

// PostFolder handles POST /api/folders.
// Adds a new watched directory for the authenticated user.
func (h *Handler) PostFolder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	var req AddFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "path is required"})
		return
	}

	// Default name to last path segment when the client omits it.
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

// DeleteFolder handles DELETE /api/folders/{folderID}.
// Removes a watched folder and cleans up all its backed-up objects from storage.
func (h *Handler) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	folderID := parseFolderID(w, r)
	if folderID == 0 {
		return
	}

	// Verify ownership and get the record before deleting.
	_, err := db.GetWatchedPathByID(r.Context(), h.db, folderID, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, ErrorResponse{Error: "folder not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to retrieve folder"})
		}
		return
	}

	// Delete each backup object from object storage before removing DB records.
	// Best-effort — log failures but continue so the DB record is always removed.
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

// PutFolder handles PUT /api/folders/{id} — rename a folder.
func (h *Handler) PutFolder(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	folderID := parseFolderID(w, r)
	if folderID == 0 {
		return
	}

	var req RenameFolderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
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

// PutAccountEmail handles PUT /api/account/email — change email address.
func (h *Handler) PutAccountEmail(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	var req ChangeEmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

// PutAccountPassword handles PUT /api/account/password — change password while logged in.
func (h *Handler) PutAccountPassword(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "current_password and new_password are required"})
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

	// Revoke all sessions so re-login is required on other devices.
	if err := db.RevokeAllUserRefreshTokens(r.Context(), h.db, userID); err != nil {
		log.Printf("warn: failed to revoke tokens after password change for user %d: %v", userID, err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteAccount handles DELETE /api/account — permanently delete the account and all data.
func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())

	var req DeleteAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	// Delete all backup objects from storage (best-effort).
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

// requireFolder is a helper used by per-folder handlers: it parses the folderID,
// verifies ownership, and returns the WatchedPath. Returns nil (response already written) on error.
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

// GetFiles handles GET /api/folders/{folderID}/files.
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

// PutSyncFiles handles PUT /api/folders/{folderID}/sync.
func (h *Handler) PutSyncFiles(w http.ResponseWriter, r *http.Request) {
	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}

	var req SyncWatchedFilesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Files == nil {
		req.Files = []FileEntry{}
	}

	watchedFiles := make([]models.WatchedFile, len(req.Files))
	for i, f := range req.Files {
		watchedFiles[i] = models.WatchedFile{
			Name:         f.Name,
			RelativePath: f.RelativePath,
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

// GetFileBackups handles GET /api/folders/{folderID}/backups.
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
	// Validate inputs before any DB access so unit tests with nil DB can cover these checks.
	relativePath := chi.URLParam(r, "*")
	if relativePath == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "relative_path is required"})
		return
	}
	if strings.Contains(relativePath, "..") || strings.Contains(filepath.Clean("/"+relativePath), "..") {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid relative_path"})
		return
	}

	checksum := r.Header.Get("X-Checksum-SHA256")
	if checksum == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "X-Checksum-SHA256 header is required"})
		return
	}

	fileSizeStr := r.Header.Get("X-File-Size")
	fileSize, fileSizeErr := strconv.ParseInt(fileSizeStr, 10, 64)
	if fileSizeErr != nil || fileSize < 0 {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "X-File-Size header is required and must be a non-negative integer"})
		return
	}

	userID := userIDFromContext(r.Context())
	wp := h.requireFolder(w, r, userID)
	if wp == nil {
		return
	}

	existing, err := db.GetFileBackup(r.Context(), h.db, wp.ID, relativePath)
	if err == nil && existing.ChecksumSHA256 == checksum {
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

	objectKey := storage.ObjectKey(userID, wp.ID, relativePath)

	// If a prior backup exists with a different key (e.g. legacy 2-part format), delete
	// the old object after the new one is written so storage doesn't accumulate orphans.
	var oldKey string
	if err == nil && existing.ObjectKey != objectKey {
		oldKey = existing.ObjectKey
	}

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

// GetFileBackup handles GET /api/folders/{folderID}/backup/*.
func (h *Handler) GetFileBackup(w http.ResponseWriter, r *http.Request) {
	// Validate path before any DB access so unit tests with nil DB can cover these checks.
	relativePath := chi.URLParam(r, "*")
	if relativePath == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "relative_path is required"})
		return
	}
	if strings.Contains(relativePath, "..") || strings.Contains(filepath.Clean("/"+relativePath), "..") {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid relative_path"})
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
	io.Copy(w, reader) //nolint:errcheck — response already started, can't change status
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

func setAuthCookies(w http.ResponseWriter, accessToken, rawRefresh string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieAccessToken,
		Value:    accessToken,
		Path:     "/",
		MaxAge:   int(session.AccessTokenTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cookieRefreshToken,
		Value:    rawRefresh,
		Path:     "/api/auth/refresh",
		MaxAge:   int(session.RefreshTokenTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearAuthCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieAccessToken,
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cookieRefreshToken,
		Path:     "/api/auth/refresh",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
