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

// newTestRouter returns a router with nil DB and storage.
// Safe for endpoints that do not touch the DB or storage: /api/health, /api/session.
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
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: "not-a-real-jwt"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp SessionResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.False(t, resp.LoggedIn)
	assert.Nil(t, resp.User)
}

func TestGetSession_WrongSigningKey(t *testing.T) {
	otherSvc := session.NewService("different-secret")
	token, err := otherSvc.CreateAccessToken(1, "alice@example.com")
	require.NoError(t, err)

	r := newTestRouter()
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp SessionResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.False(t, resp.LoggedIn)
}

func TestGetSession_ValidAccessToken(t *testing.T) {
	r, svc := newTestRouterWithSvc()

	token, err := svc.CreateAccessToken(42, "bob@example.com")
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp SessionResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.True(t, resp.LoggedIn)
	require.NotNil(t, resp.User)
	assert.Equal(t, int64(42), resp.User.ID)
	assert.Equal(t, "bob@example.com", resp.User.Email)
}

func TestGetSession_NoCookie(t *testing.T) {
	r := newTestRouter()

	// Sending an Authorization header must NOT be treated as a session anymore.
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp SessionResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.False(t, resp.LoggedIn)
}

func TestCORSPreflightRequest(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodOptions, "/api/session", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "http://localhost:3000", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
}

func TestSessionSvc_GenerateAndHashRefreshToken(t *testing.T) {
	raw, hash, err := session.GenerateRefreshToken()
	require.NoError(t, err)
	assert.NotEmpty(t, raw)
	assert.NotEmpty(t, hash)
	assert.NotEqual(t, raw, hash)

	// HashToken must be deterministic
	assert.Equal(t, hash, session.HashToken(raw))

	// Different tokens must produce different hashes
	raw2, hash2, err := session.GenerateRefreshToken()
	require.NoError(t, err)
	assert.NotEqual(t, raw, raw2)
	assert.NotEqual(t, hash, hash2)
}

// ---- File endpoint auth tests ----
// These tests use a nil DB pool, so they only verify that the requireAuth
// middleware fires correctly. Actual DB behaviour is covered in integration tests.

func TestFileEndpoints_RequireAuth(t *testing.T) {
	r := newTestRouter()

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/files/path"},
		{http.MethodPut, "/api/files/path"},
		{http.MethodGet, "/api/files/"},
		{http.MethodPut, "/api/files/sync"},
	}

	for _, ep := range endpoints {
		req := httptest.NewRequest(ep.method, ep.path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s should require auth", ep.method, ep.path)

		var errResp ErrorResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
		assert.NotEmpty(t, errResp.Error)
	}
}

func TestFileEndpoints_InvalidToken(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/files/path", nil)
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: "not-a-valid-jwt"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}


func TestSessionSvc_AccessTokenRoundtrip(t *testing.T) {
	svc := session.NewService("unit-test-secret")

	token, err := svc.CreateAccessToken(7, "test@example.com")
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	claims, err := svc.ValidateAccessToken(token)
	require.NoError(t, err)
	assert.Equal(t, int64(7), claims.UserID)
	assert.Equal(t, "test@example.com", claims.Email)
}
