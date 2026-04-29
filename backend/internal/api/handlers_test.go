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
	req.Header.Set("X-Checksum-SHA256", "a3f5b2c1d4e6789012345678901234567890123456789012345678901234abcd")
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
		req.Header.Set("X-Checksum-SHA256", "a3f5b2c1d4e6789012345678901234567890123456789012345678901234abcd")
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

// ---- validateRelativePath unit tests ----

func TestValidateRelativePath_Valid(t *testing.T) {
	cases := []string{
		"file.txt",
		"a/b/c.txt",
		"photos/2024/img.jpg",
		"dir/sub/deep/file.bin",
	}
	for _, p := range cases {
		got, err := validateRelativePath(p)
		assert.NoError(t, err, "expected %q to be valid", p)
		assert.Equal(t, p, got)
	}
}

func TestValidateRelativePath_Invalid(t *testing.T) {
	cases := []struct {
		input string
		desc  string
	}{
		{"", "empty"},
		{"../etc/passwd", "dotdot escape"},
		{"foo/../bar", "dotdot in middle"},
		{"/absolute", "absolute path"},
		{"foo\\bar", "backslash"},
		{"foo//bar", "double slash (not canonical)"},
		{".", "single dot"},
	}
	for _, tc := range cases {
		_, err := validateRelativePath(tc.input)
		assert.Error(t, err, "expected %q (%s) to be invalid", tc.input, tc.desc)
	}
}

// ---- validatePassword unit tests ----

func TestValidatePassword_Valid(t *testing.T) {
	assert.NoError(t, validatePassword("pass"))       // exactly min length (4)
	assert.NoError(t, validatePassword("password"))
	assert.NoError(t, validatePassword("C0mpl3x!Pass"))
}

func TestValidatePassword_TooShort(t *testing.T) {
	assert.Error(t, validatePassword("abc"))
}

func TestValidatePassword_TooLong(t *testing.T) {
	assert.Error(t, validatePassword(strings.Repeat("x", 73))) // over bcrypt limit of 72
}

func TestValidatePassword_NULByte(t *testing.T) {
	assert.Error(t, validatePassword("pass\x00word"))
}

// ---- Auth endpoint request validation (no DB needed) ----

func TestPostLogin_MissingFields(t *testing.T) {
	r := newTestRouter()

	cases := []struct {
		body string
		desc string
	}{
		{`{"email":"","password":""}`, "both empty"},
		{`{"email":"x@y.com","password":""}`, "missing password"},
		{`{"email":"","password":"hunter2"}`, "missing email"},
	}
	for _, tc := range cases {
		req := withAllowedOrigin(httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(tc.body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusBadRequest, rec.Code, tc.desc)
	}
}

func TestPostRegister_InvalidEmail(t *testing.T) {
	r := newTestRouter()

	body := strings.NewReader(`{"email":"not-an-email","password":"password"}`)
	req := withAllowedOrigin(httptest.NewRequest(http.MethodPost, "/api/auth/register", body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "email")
}

func TestPostRegister_WeakPassword(t *testing.T) {
	r := newTestRouter()

	body := strings.NewReader(`{"email":"user@example.com","password":"ab"}`)
	req := withAllowedOrigin(httptest.NewRequest(http.MethodPost, "/api/auth/register", body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "password")
}

func TestPostForgotPassword_MissingEmail(t *testing.T) {
	r := newTestRouter()

	body := strings.NewReader(`{"email":""}`)
	req := withAllowedOrigin(httptest.NewRequest(http.MethodPost, "/api/auth/forgot-password", body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPostResetPassword_MissingToken(t *testing.T) {
	r := newTestRouter()

	body := strings.NewReader(`{"reset_token":"","new_password":"newpassword"}`)
	req := withAllowedOrigin(httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPostResetPassword_WeakNewPassword(t *testing.T) {
	r := newTestRouter()

	body := strings.NewReader(`{"reset_token":"sometoken","new_password":"x"}`)
	req := withAllowedOrigin(httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "password")
}

// ---- Folder endpoint validation (auth required, validation fires before DB) ----

func TestPostFolder_MissingPath(t *testing.T) {
	r, svc := newTestRouterWithSvc()
	token, err := svc.CreateAccessToken(1, "user@example.com")
	require.NoError(t, err)

	body := strings.NewReader(`{"path":""}`)
	req := withAllowedOrigin(httptest.NewRequest(http.MethodPost, "/api/folders/", body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "path")
}

func TestPutFolder_EmptyName(t *testing.T) {
	r, svc := newTestRouterWithSvc()
	token, err := svc.CreateAccessToken(1, "user@example.com")
	require.NoError(t, err)

	body := strings.NewReader(`{"name":""}`)
	req := withAllowedOrigin(httptest.NewRequest(http.MethodPut, "/api/folders/1/", body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var errResp ErrorResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&errResp))
	assert.Contains(t, errResp.Error, "name")
}

func TestPutFolder_InvalidFolderID(t *testing.T) {
	r, svc := newTestRouterWithSvc()
	token, err := svc.CreateAccessToken(1, "user@example.com")
	require.NoError(t, err)

	req := withAllowedOrigin(httptest.NewRequest(http.MethodPut, "/api/folders/abc/", strings.NewReader(`{"name":"x"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUploadEndpoint_FileSizeTooLarge(t *testing.T) {
	r, svc := newTestRouterWithSvc()
	token, err := svc.CreateAccessToken(1, "user@example.com")
	require.NoError(t, err)

	req := withAllowedOrigin(httptest.NewRequest(http.MethodPut, "/api/folders/1/backup/test.txt", nil))
	req.AddCookie(&http.Cookie{Name: cookieAccessToken, Value: token})
	req.Header.Set("X-Checksum-SHA256", "a3f5b2c1d4e6789012345678901234567890123456789012345678901234abcd")
	req.Header.Set("X-File-Size", "10995116277761") // 10 GiB + 1 byte
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
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
