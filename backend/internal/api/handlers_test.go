package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
)

// newTestRouter returns a router with no database (nil pool).
// Safe for endpoints that do not touch the DB: /api/health, /api/session.
func newTestRouter() http.Handler {
	svc := session.NewService("test-secret-for-unit-tests")
	return NewRouter(nil, svc)
}

// newTestRouterWithSvc returns a router and the session service for token creation.
func newTestRouterWithSvc() (http.Handler, *session.Service) {
	svc := session.NewService("test-secret-for-unit-tests")
	return NewRouter(nil, svc), svc
}

func TestGetHealth(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var resp HealthResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.Equal(t, "ok", resp.Status)
	assert.NotEmpty(t, resp.Version)
}

func TestGetSession_NoToken(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp SessionResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.False(t, resp.LoggedIn)
	assert.Nil(t, resp.User)
}

func TestGetSession_InvalidToken(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp SessionResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.False(t, resp.LoggedIn)
	assert.Nil(t, resp.User)
}

func TestGetSession_WrongSigningKey(t *testing.T) {
	// Token signed with a different secret should not be trusted.
	otherSvc := session.NewService("different-secret")
	token, err := otherSvc.CreateToken(1, "alice", "alice@example.com")
	require.NoError(t, err)

	r := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp SessionResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.False(t, resp.LoggedIn)
}

func TestGetSession_ValidToken(t *testing.T) {
	r, svc := newTestRouterWithSvc()

	token, err := svc.CreateToken(42, "bob", "bob@example.com")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp SessionResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.True(t, resp.LoggedIn)
	require.NotNil(t, resp.User)
	assert.Equal(t, int64(42), resp.User.ID)
	assert.Equal(t, "bob", resp.User.Username)
	assert.Equal(t, "bob@example.com", resp.User.Email)
}

func TestGetSession_MalformedAuthHeader(t *testing.T) {
	r := newTestRouter()

	for _, header := range []string{"Token abc", "Bearer", "Basic dXNlcjpwYXNz"} {
		req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code, "header: %s", header)

		var resp SessionResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.False(t, resp.LoggedIn, "header: %s", header)
	}
}

func TestCORSPreflightRequest(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodOptions, "/api/session", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
}
