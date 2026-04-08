package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/models"
	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	db         *pgxpool.Pool // may be nil in unit tests that don't touch the DB
	sessionSvc *session.Service
}

// NewHandler creates a Handler with the provided dependencies.
func NewHandler(pool *pgxpool.Pool, sessionSvc *session.Service) *Handler {
	return &Handler{db: pool, sessionSvc: sessionSvc}
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
// Contains both a short-lived access token and a long-lived refresh token.
type AuthResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	User         UserInfo `json:"user"`
}

// RefreshResponse is returned by POST /api/auth/refresh.
type RefreshResponse struct {
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	User         UserInfo `json:"user"`
}

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

// RefreshRequest is the body expected by POST /api/auth/refresh.
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// LogoutRequest is the body expected by POST /api/auth/logout.
type LogoutRequest struct {
	RefreshToken string `json:"refresh_token"`
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

// ---- Handlers ----

// GetHealth handles GET /api/health.
func (h *Handler) GetHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok", Version: "0.1.0"})
}

// GetSession handles GET /api/session.
// Returns current session state based on Authorization: Bearer <access_token>.
// Always returns 200 — missing/invalid tokens yield {logged_in: false}.
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		writeJSON(w, http.StatusOK, SessionResponse{LoggedIn: false})
		return
	}

	claims, err := h.sessionSvc.ValidateAccessToken(token)
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

	writeJSON(w, http.StatusOK, AuthResponse{
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
		User:         UserInfo{ID: user.ID, Email: user.Email},
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

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to process password"})
		return
	}

	user := &models.User{
		Email:        req.Email,
		PasswordHash: string(hash),
	}
	if err := db.CreateUser(r.Context(), h.db, user); err != nil {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "email already registered"})
		return
	}

	accessToken, rawRefresh, err := h.issueTokenPair(r, user)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create session"})
		return
	}

	writeJSON(w, http.StatusCreated, AuthResponse{
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
		User:         UserInfo{ID: user.ID, Email: user.Email},
	})
}

// PostRefresh handles POST /api/auth/refresh.
// Validates the provided refresh token, rotates it (revoke old, issue new pair).
func (h *Handler) PostRefresh(w http.ResponseWriter, r *http.Request) {
	var req RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "refresh_token is required"})
		return
	}

	hash := session.HashToken(req.RefreshToken)
	rt, err := db.GetRefreshTokenByHash(r.Context(), h.db, hash)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid refresh token"})
		return
	}

	// Theft detection: revoked token re-presented → revoke all tokens for this user.
	if rt.Revoked {
		_ = db.RevokeAllUserRefreshTokens(r.Context(), h.db, rt.UserID)
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "refresh token reuse detected — all sessions revoked"})
		return
	}

	if time.Now().After(rt.ExpiresAt) {
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

	writeJSON(w, http.StatusOK, RefreshResponse{
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
		User:         UserInfo{ID: user.ID, Email: user.Email},
	})
}

// PostLogout handles POST /api/auth/logout.
// Revokes the provided refresh token. Idempotent — succeeds even if already revoked.
func (h *Handler) PostLogout(w http.ResponseWriter, r *http.Request) {
	var req LogoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "refresh_token is required"})
		return
	}

	hash := session.HashToken(req.RefreshToken)
	rt, err := db.GetRefreshTokenByHash(r.Context(), h.db, hash)
	if err == nil && !rt.Revoked {
		// Best-effort revocation — ignore error so logout is always idempotent.
		_ = db.RevokeRefreshToken(r.Context(), h.db, rt.ID)
	}

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

	genericMsg := "If the account exists, a reset token has been issued."

	user, err := db.GetUserByEmail(r.Context(), h.db, req.Email)
	if err != nil {
		// Don't reveal whether the email exists.
		writeJSON(w, http.StatusOK, ForgotPasswordResponse{Message: genericMsg})
		return
	}

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

	newHash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to process password"})
		return
	}

	if err := db.UpdateUserPassword(r.Context(), h.db, prt.UserID, string(newHash)); err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update password"})
		return
	}

	// Consume the reset token so it cannot be replayed.
	_ = db.MarkPasswordResetTokenUsed(r.Context(), h.db, prt.ID)

	// Revoke all active sessions — user must log in again with the new password.
	_ = db.RevokeAllUserRefreshTokens(r.Context(), h.db, prt.UserID)

	writeJSON(w, http.StatusOK, map[string]string{"message": "Password updated successfully. Please log in again."})
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

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	parts := strings.SplitN(auth, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return ""
	}
	return parts[1]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
