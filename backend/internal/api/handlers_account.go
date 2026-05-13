package api

import (
	"errors"
	"log"
	"net/http"
	"net/mail"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/jackc/pgx/v5/pgconn"
)

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
			// Return generic 400 — intentionally no "email already in use" message to prevent enumeration.
			writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "could not update email"})
		} else {
			writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to update email"})
		}
		return
	}

	if err := db.RevokeAllUserRefreshTokens(r.Context(), h.db, userID); err != nil {
		log.Printf("warn: failed to revoke tokens after email change for user %d: %v", userID, err)
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
