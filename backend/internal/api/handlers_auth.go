package api

import (
	"errors"
	"net/http"
	"net/mail"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/models"
	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
	"github.com/jackc/pgx/v5/pgconn"
)

// GetHealth handles GET /api/health.
func (h *Handler) GetHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok", Version: Version})
}

// GetSession handles GET /api/session.
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
