package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/models"
	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Handler holds shared dependencies for all HTTP handlers.
type Handler struct {
	db         *pgxpool.Pool  // may be nil in unit tests that don't touch the DB
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
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// AuthResponse is returned after a successful login or registration.
type AuthResponse struct {
	Token string   `json:"token"`
	User  UserInfo `json:"user"`
}

// ErrorResponse wraps an error message returned to the client.
type ErrorResponse struct {
	Error string `json:"error"`
}

// LoginRequest is the body expected by POST /api/auth/login.
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// RegisterRequest is the body expected by POST /api/auth/register.
type RegisterRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

// ---- Handlers ----

// GetHealth handles GET /api/health.
func (h *Handler) GetHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok", Version: "0.1.0"})
}

// GetSession handles GET /api/session.
// Returns the current session state based on the Authorization: Bearer <token> header.
func (h *Handler) GetSession(w http.ResponseWriter, r *http.Request) {
	token := extractBearerToken(r)
	if token == "" {
		writeJSON(w, http.StatusOK, SessionResponse{LoggedIn: false})
		return
	}

	claims, err := h.sessionSvc.ValidateToken(token)
	if err != nil {
		writeJSON(w, http.StatusOK, SessionResponse{LoggedIn: false})
		return
	}

	writeJSON(w, http.StatusOK, SessionResponse{
		LoggedIn: true,
		User: &UserInfo{
			ID:       claims.UserID,
			Username: claims.Username,
			Email:    claims.Email,
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
	if req.Username == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "username and password are required"})
		return
	}

	user, err := db.GetUserByUsername(r.Context(), h.db, req.Username)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid credentials"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		writeJSON(w, http.StatusUnauthorized, ErrorResponse{Error: "invalid credentials"})
		return
	}

	tokenStr, err := h.sessionSvc.CreateToken(user.ID, user.Username, user.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create session"})
		return
	}

	writeJSON(w, http.StatusOK, AuthResponse{
		Token: tokenStr,
		User:  UserInfo{ID: user.ID, Username: user.Username, Email: user.Email},
	})
}

// PostRegister handles POST /api/auth/register.
func (h *Handler) PostRegister(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "invalid request body"})
		return
	}
	if req.Username == "" || req.Email == "" || req.Password == "" {
		writeJSON(w, http.StatusBadRequest, ErrorResponse{Error: "username, email, and password are required"})
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to process password"})
		return
	}

	user := &models.User{
		Username:     req.Username,
		Email:        req.Email,
		PasswordHash: string(hash),
	}
	if err := db.CreateUser(r.Context(), h.db, user); err != nil {
		writeJSON(w, http.StatusConflict, ErrorResponse{Error: "username or email already exists"})
		return
	}

	tokenStr, err := h.sessionSvc.CreateToken(user.ID, user.Username, user.Email)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ErrorResponse{Error: "failed to create session"})
		return
	}

	writeJSON(w, http.StatusCreated, AuthResponse{
		Token: tokenStr,
		User:  UserInfo{ID: user.ID, Username: user.Username, Email: user.Email},
	})
}

// ---- Helpers ----

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
