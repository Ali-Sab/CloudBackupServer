package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
)

// newTestRouter returns a router with nil DB and storage.
// Safe for endpoints that do not touch the DB or storage: /api/health, /api/session.
func newTestRouter() http.Handler {
	svc := session.NewService("test-secret-for-unit-tests")
	return NewRouter(nil, svc, nil)
}

// newTestRouterWithSvc returns a router and the session service for token creation.
func newTestRouterWithSvc() (http.Handler, *session.Service) {
	svc := session.NewService("test-secret-for-unit-tests")
	return NewRouter(nil, svc, nil), svc
}

// withAllowedOrigin sets a same-origin header so the CSRF middleware lets the request through.
// Use this on every mutating request in tests.
func withAllowedOrigin(req *http.Request) *http.Request {
	req.Header.Set("Origin", "http://localhost:5173")
	return req
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

func TestGetSession_BearerHeaderIgnored(t *testing.T) {
	r := newTestRouter()

	// Cookie-only auth: an Authorization header must NOT be treated as a session.
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp SessionResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.False(t, resp.LoggedIn)
}

func TestCORSPreflight_AllowedOrigin(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodOptions, "/api/session", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "http://localhost:5173", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORSPreflight_DisallowedOrigin(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodOptions, "/api/session", nil)
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
}

func TestCSRF_BlocksUnknownOrigin(t *testing.T) {
	r := newTestRouter()

	body := strings.NewReader(`{"email":"x@y.com","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
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
		{http.MethodGet, "/api/folders/"},
		{http.MethodPost, "/api/folders/"},
		{http.MethodDelete, "/api/folders/1/"},
		{http.MethodPut, "/api/folders/1/"},
		{http.MethodGet, "/api/folders/1/files"},
		{http.MethodPut, "/api/folders/1/sync"},
		{http.MethodGet, "/api/folders/1/backups"},
		{http.MethodPut, "/api/folders/1/backup/some/file.txt"},
		{http.MethodGet, "/api/folders/1/backup/some/file.txt"},
	}

	for _, ep := range endpoints {
		req := withAllowedOrigin(httptest.NewRequest(ep.method, ep.path, nil))
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

	req := httptest.NewRequest(http.MethodGet, "/api/folders/", nil)
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: "not-a-valid-jwt"})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// ---- Backup endpoint validation tests ----
// These tests confirm that validation fires before any DB or storage access.

func TestUploadEndpoint_MissingChecksumHeader(t *testing.T) {
	r, svc := newTestRouterWithSvc()
	token, err := svc.CreateAccessToken(1, "user@example.com")
	require.NoError(t, err)

	req := withAllowedOrigin(httptest.NewRequest(http.MethodPut, "/api/folders/1/backup/test.txt", nil))
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
	req.Header.Set("X-File-Size", "100")
	// X-Checksum-SHA256 intentionally omitted
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "X-Checksum-SHA256")
}

func TestUploadEndpoint_MissingFileSizeHeader(t *testing.T) {
	r, svc := newTestRouterWithSvc()
	token, err := svc.CreateAccessToken(1, "user@example.com")
	require.NoError(t, err)

	req := withAllowedOrigin(httptest.NewRequest(http.MethodPut, "/api/folders/1/backup/test.txt", nil))
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
	req.Header.Set("X-Checksum-SHA256", "abc123")
	// X-File-Size intentionally omitted
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "X-File-Size")
}

func TestUploadEndpoint_PathTraversal(t *testing.T) {
	r, svc := newTestRouterWithSvc()
	token, err := svc.CreateAccessToken(1, "user@example.com")
	require.NoError(t, err)

	for _, path := range []string{"../etc/passwd", "foo/../bar/baz", "a/../../secret"} {
		req := withAllowedOrigin(httptest.NewRequest(http.MethodPut, "/api/folders/1/backup/"+path, nil))
		req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
		req.Header.Set("X-Checksum-SHA256", "abc123")
		req.Header.Set("X-File-Size", "10")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code, "path %q should be rejected", path)
	}
}

func TestUploadEndpoint_EmptyPath(t *testing.T) {
	r, svc := newTestRouterWithSvc()
	token, err := svc.CreateAccessToken(1, "user@example.com")
	require.NoError(t, err)

	req := withAllowedOrigin(httptest.NewRequest(http.MethodPut, "/api/folders/1/backup/", nil))
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
	req.Header.Set("X-Checksum-SHA256", "abc123")
	req.Header.Set("X-File-Size", "10")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestDownloadEndpoint_PathTraversal(t *testing.T) {
	r, svc := newTestRouterWithSvc()
	token, err := svc.CreateAccessToken(1, "user@example.com")
	require.NoError(t, err)

	for _, path := range []string{"../etc/passwd", "foo/../bar", "a/../../secret"} {
		req := httptest.NewRequest(http.MethodGet, "/api/folders/1/backup/"+path, nil)
		req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code, "path %q should be rejected", path)
	}
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

func TestAccountEndpoints_RequireAuth(t *testing.T) {
	r := newTestRouter()

	endpoints := []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/api/account/email"},
		{http.MethodPut, "/api/account/password"},
		{http.MethodDelete, "/api/account"},
	}

	for _, ep := range endpoints {
		req := withAllowedOrigin(httptest.NewRequest(ep.method, ep.path, nil))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s should require auth", ep.method, ep.path)

		var errResp ErrorResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
		assert.NotEmpty(t, errResp.Error)
	}
}
