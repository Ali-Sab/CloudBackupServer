//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ali-sab/cloudbackupserver/backend/internal/api"
	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
)

// setupTestServer creates a full server backed by a real database.
// TEST_DATABASE_URL must be set; otherwise the test is skipped.
// All rows inserted during the test are truncated in t.Cleanup.
func setupTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration tests")
	}

	require.NoError(t, db.RunMigrations(databaseURL), "migrations must succeed")

	pool, err := db.Connect(context.Background(), databaseURL)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			"TRUNCATE watched_files, watched_paths, password_reset_tokens, refresh_tokens, users RESTART IDENTITY CASCADE")
		pool.Close()
	})

	svc := session.NewService("integration-test-jwt-secret")
	router := api.NewRouter(pool, svc)
	return httptest.NewServer(router)
}

// ---- helpers ----

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	require.NoError(t, err)
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(v))
}

func authGet(t *testing.T, url, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func authPut(t *testing.T, url, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// registerAndLogin is a convenience helper that registers a user and returns their access token.
func registerAndLogin(t *testing.T, srv *httptest.Server, email, password string) string {
	t.Helper()
	resp := postJSON(t, srv.URL+"/api/auth/register",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, password))
	var auth api.AuthResponse
	decodeJSON(t, resp, &auth)
	require.NotEmpty(t, auth.AccessToken, "registration must return an access token")
	return auth.AccessToken
}

// ---- tests ----

func TestIntegration_HealthCheck(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/health")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIntegration_Register(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/auth/register",
		`{"email":"alice@example.com","password":"secret123"}`)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var auth api.AuthResponse
	decodeJSON(t, resp, &auth)
	assert.NotEmpty(t, auth.AccessToken)
	assert.NotEmpty(t, auth.RefreshToken)
	assert.Equal(t, "alice@example.com", auth.User.Email)
}

func TestIntegration_RegisterDuplicateEmail(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	body := `{"email":"dup@example.com","password":"pass"}`
	postJSON(t, srv.URL+"/api/auth/register", body)
	resp := postJSON(t, srv.URL+"/api/auth/register", body)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestIntegration_LoginSuccess(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	postJSON(t, srv.URL+"/api/auth/register",
		`{"email":"bob@example.com","password":"mypassword"}`)

	resp := postJSON(t, srv.URL+"/api/auth/login",
		`{"email":"bob@example.com","password":"mypassword"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var auth api.AuthResponse
	decodeJSON(t, resp, &auth)
	assert.NotEmpty(t, auth.AccessToken)
	assert.NotEmpty(t, auth.RefreshToken)
}

func TestIntegration_LoginWrongPassword(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	postJSON(t, srv.URL+"/api/auth/register",
		`{"email":"carol@example.com","password":"correct"}`)

	resp := postJSON(t, srv.URL+"/api/auth/login", `{"email":"carol@example.com","password":"wrong"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestIntegration_SessionFlow(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	// Unauthenticated
	resp, err := http.Get(srv.URL + "/api/session")
	require.NoError(t, err)
	var s api.SessionResponse
	decodeJSON(t, resp, &s)
	assert.False(t, s.LoggedIn)

	// Register
	regResp := postJSON(t, srv.URL+"/api/auth/register",
		`{"email":"dave@example.com","password":"pass456"}`)
	var auth api.AuthResponse
	decodeJSON(t, regResp, &auth)

	// Authenticated session check
	resp = authGet(t, srv.URL+"/api/session", auth.AccessToken)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var authed api.SessionResponse
	decodeJSON(t, resp, &authed)
	assert.True(t, authed.LoggedIn)
	require.NotNil(t, authed.User)
	assert.Equal(t, "dave@example.com", authed.User.Email)
}

func TestIntegration_RefreshTokenRotation(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	// Register and get initial tokens
	regResp := postJSON(t, srv.URL+"/api/auth/register",
		`{"email":"eve@example.com","password":"pass"}`)
	var auth api.AuthResponse
	decodeJSON(t, regResp, &auth)
	require.NotEmpty(t, auth.RefreshToken)

	// Refresh → get new pair
	refreshResp := postJSON(t, srv.URL+"/api/auth/refresh",
		fmt.Sprintf(`{"refresh_token":%q}`, auth.RefreshToken))
	assert.Equal(t, http.StatusOK, refreshResp.StatusCode)

	var refreshed api.RefreshResponse
	decodeJSON(t, refreshResp, &refreshed)
	assert.NotEmpty(t, refreshed.AccessToken)
	assert.NotEmpty(t, refreshed.RefreshToken)
	// Refresh token must be different (rotation). Access token may be identical
	// if both are issued within the same second (same exp/iat), so we don't assert it.
	assert.NotEqual(t, auth.RefreshToken, refreshed.RefreshToken)

	// Old refresh token must now be rejected
	resp := postJSON(t, srv.URL+"/api/auth/refresh",
		fmt.Sprintf(`{"refresh_token":%q}`, auth.RefreshToken))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestIntegration_TheftDetection(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	// Register
	regResp := postJSON(t, srv.URL+"/api/auth/register",
		`{"email":"frank@example.com","password":"pass"}`)
	var auth api.AuthResponse
	decodeJSON(t, regResp, &auth)
	original := auth.RefreshToken

	// Rotate once
	postJSON(t, srv.URL+"/api/auth/refresh",
		fmt.Sprintf(`{"refresh_token":%q}`, original))

	// Re-present the already-revoked original → theft detection
	resp := postJSON(t, srv.URL+"/api/auth/refresh",
		fmt.Sprintf(`{"refresh_token":%q}`, original))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var errResp api.ErrorResponse
	decodeJSON(t, resp, &errResp)
	assert.Contains(t, errResp.Error, "reuse detected")
}

func TestIntegration_Logout(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	regResp := postJSON(t, srv.URL+"/api/auth/register",
		`{"email":"grace@example.com","password":"pass"}`)
	var auth api.AuthResponse
	decodeJSON(t, regResp, &auth)

	// Logout
	logoutResp := postJSON(t, srv.URL+"/api/auth/logout",
		fmt.Sprintf(`{"refresh_token":%q}`, auth.RefreshToken))
	assert.Equal(t, http.StatusNoContent, logoutResp.StatusCode)

	// Refresh after logout must fail
	resp := postJSON(t, srv.URL+"/api/auth/refresh",
		fmt.Sprintf(`{"refresh_token":%q}`, auth.RefreshToken))
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Logout again must still succeed (idempotent)
	logoutResp2 := postJSON(t, srv.URL+"/api/auth/logout",
		fmt.Sprintf(`{"refresh_token":%q}`, auth.RefreshToken))
	assert.Equal(t, http.StatusNoContent, logoutResp2.StatusCode)
}

func TestIntegration_ForgotAndResetPassword(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	postJSON(t, srv.URL+"/api/auth/register",
		`{"email":"henry@example.com","password":"oldpass"}`)

	// Forgot password
	fpResp := postJSON(t, srv.URL+"/api/auth/forgot-password", `{"email":"henry@example.com"}`)
	assert.Equal(t, http.StatusOK, fpResp.StatusCode)

	var fp api.ForgotPasswordResponse
	decodeJSON(t, fpResp, &fp)
	require.NotEmpty(t, fp.ResetToken, "dev mode must return reset_token")

	// Reset password
	resetResp := postJSON(t, srv.URL+"/api/auth/reset-password",
		fmt.Sprintf(`{"reset_token":%q,"new_password":"newpass"}`, fp.ResetToken))
	assert.Equal(t, http.StatusOK, resetResp.StatusCode)

	// Old password must no longer work
	resp := postJSON(t, srv.URL+"/api/auth/login", `{"email":"henry@example.com","password":"oldpass"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// New password must work
	resp = postJSON(t, srv.URL+"/api/auth/login", `{"email":"henry@example.com","password":"newpass"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIntegration_ResetTokenSingleUse(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	postJSON(t, srv.URL+"/api/auth/register",
		`{"email":"iris@example.com","password":"pass"}`)

	fpResp := postJSON(t, srv.URL+"/api/auth/forgot-password", `{"email":"iris@example.com"}`)
	var fp api.ForgotPasswordResponse
	decodeJSON(t, fpResp, &fp)

	// Use it once
	postJSON(t, srv.URL+"/api/auth/reset-password",
		fmt.Sprintf(`{"reset_token":%q,"new_password":"newpass"}`, fp.ResetToken))

	// Use it again — must fail
	resp := postJSON(t, srv.URL+"/api/auth/reset-password",
		fmt.Sprintf(`{"reset_token":%q,"new_password":"anotherpass"}`, fp.ResetToken))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestIntegration_ForgotPasswordUnknownUser(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	// Unknown email — must return 200 with no reset_token (prevents enumeration)
	resp := postJSON(t, srv.URL+"/api/auth/forgot-password", `{"email":"nobody@example.com"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var fp api.ForgotPasswordResponse
	decodeJSON(t, resp, &fp)
	assert.Empty(t, fp.ResetToken)
	assert.NotEmpty(t, fp.Message)
}

// ---- File endpoint integration tests ----

func TestIntegration_FilesEndpoints_RequireAuth(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/files/path", ""},
		{http.MethodPut, "/api/files/path", `{"path":"/tmp"}`},
		{http.MethodGet, "/api/files/", ""},
		{http.MethodPut, "/api/files/sync", `{"files":[]}`},
	} {
		var req *http.Request
		if tc.body != "" {
			req, _ = http.NewRequest(tc.method, srv.URL+tc.path, bytes.NewBufferString(tc.body))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req, _ = http.NewRequest(tc.method, srv.URL+tc.path, nil)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s %s", tc.method, tc.path)
	}
}

func TestIntegration_WatchedPath_SetAndGet(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	token := registerAndLogin(t, srv, "path-user@example.com", "pass")

	// GET before any path is set → 404
	resp := authGet(t, srv.URL+"/api/files/path", token)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// PUT a path
	resp = authPut(t, srv.URL+"/api/files/path", token, `{"path":"/home/user/documents"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var wp api.WatchedPathResponse
	decodeJSON(t, resp, &wp)
	assert.Equal(t, "/home/user/documents", wp.Path)
	assert.NotZero(t, wp.ID)

	// GET now returns the saved path
	resp = authGet(t, srv.URL+"/api/files/path", token)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var got api.WatchedPathResponse
	decodeJSON(t, resp, &got)
	assert.Equal(t, "/home/user/documents", got.Path)
}

func TestIntegration_WatchedPath_Upsert(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	token := registerAndLogin(t, srv, "upsert-user@example.com", "pass")

	authPut(t, srv.URL+"/api/files/path", token, `{"path":"/old/path"}`)
	resp := authPut(t, srv.URL+"/api/files/path", token, `{"path":"/new/path"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var wp api.WatchedPathResponse
	decodeJSON(t, resp, &wp)
	assert.Equal(t, "/new/path", wp.Path)

	// GET must return the updated path, not both
	resp = authGet(t, srv.URL+"/api/files/path", token)
	var got api.WatchedPathResponse
	decodeJSON(t, resp, &got)
	assert.Equal(t, "/new/path", got.Path)
}

func TestIntegration_WatchedPath_MissingPath(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	token := registerAndLogin(t, srv, "nopath-user@example.com", "pass")

	resp := authPut(t, srv.URL+"/api/files/path", token, `{"path":""}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestIntegration_SyncFiles_NoPath(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	token := registerAndLogin(t, srv, "syncnopath@example.com", "pass")

	// Sync without setting a path first → 404
	resp := authPut(t, srv.URL+"/api/files/sync", token, `{"files":[]}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_SyncAndGetFiles(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	token := registerAndLogin(t, srv, "sync-user@example.com", "pass")

	// Set a watched path first
	authPut(t, srv.URL+"/api/files/path", token, `{"path":"/home/user/backups"}`)

	// GET files before any sync → empty list
	resp := authGet(t, srv.URL+"/api/files/", token)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var empty api.WatchedFilesResponse
	decodeJSON(t, resp, &empty)
	assert.Empty(t, empty.Files)

	// Sync a set of files (include relative_path)
	syncBody := `{"files":[
		{"name":"notes.txt","relative_path":"notes.txt","is_directory":false,"size":1024,"modified_ms":1700000000000},
		{"name":"photos","relative_path":"photos","is_directory":true,"size":0,"modified_ms":1700000001000}
	]}`
	resp = authPut(t, srv.URL+"/api/files/sync", token, syncBody)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// GET files → ordered by relative_path ASC
	resp = authGet(t, srv.URL+"/api/files/", token)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var files api.WatchedFilesResponse
	decodeJSON(t, resp, &files)
	require.Len(t, files.Files, 2)
	// relative_path ASC: "notes.txt" < "photos"
	assert.Equal(t, "notes.txt", files.Files[0].Name)
	assert.Equal(t, "notes.txt", files.Files[0].RelativePath)
	assert.Equal(t, int64(1024), files.Files[0].Size)
	assert.Equal(t, "photos", files.Files[1].Name)
	assert.True(t, files.Files[1].IsDirectory)

	// Re-sync with different files — must replace, not append
	resp = authPut(t, srv.URL+"/api/files/sync", token, `{"files":[
		{"name":"archive.zip","relative_path":"archive.zip","is_directory":false,"size":4096,"modified_ms":1700000002000}
	]}`)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp = authGet(t, srv.URL+"/api/files/", token)
	var replaced api.WatchedFilesResponse
	decodeJSON(t, resp, &replaced)
	require.Len(t, replaced.Files, 1)
	assert.Equal(t, "archive.zip", replaced.Files[0].Name)
}

func TestIntegration_SyncFiles_WithRelativePath(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	token := registerAndLogin(t, srv, "relpath@example.com", "pass")
	authPut(t, srv.URL+"/api/files/path", token, `{"path":"/home/user/docs"}`)

	// Sync a tree: root file, a subdirectory, and a file inside that subdirectory
	syncBody := `{"files":[
		{"name":"readme.txt","relative_path":"readme.txt","is_directory":false,"size":512,"modified_ms":1000},
		{"name":"src","relative_path":"src","is_directory":true,"size":0,"modified_ms":2000},
		{"name":"main.go","relative_path":"src/main.go","is_directory":false,"size":2048,"modified_ms":3000}
	]}`
	resp := authPut(t, srv.URL+"/api/files/sync", token, syncBody)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp = authGet(t, srv.URL+"/api/files/", token)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var result api.WatchedFilesResponse
	decodeJSON(t, resp, &result)
	require.Len(t, result.Files, 3)

	// Index by relative_path for easy assertion
	byPath := make(map[string]string) // relative_path → name
	bySize := make(map[string]int64)
	byIsDir := make(map[string]bool)
	for _, f := range result.Files {
		byPath[f.RelativePath] = f.Name
		bySize[f.RelativePath] = f.Size
		byIsDir[f.RelativePath] = f.IsDirectory
	}
	assert.Equal(t, "readme.txt", byPath["readme.txt"])
	assert.Equal(t, "src", byPath["src"])
	assert.True(t, byIsDir["src"])
	assert.Equal(t, "main.go", byPath["src/main.go"])
	assert.Equal(t, int64(2048), bySize["src/main.go"])
}

func TestIntegration_SyncFiles_RelativePathOrdering(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	token := registerAndLogin(t, srv, "ordering@example.com", "pass")
	authPut(t, srv.URL+"/api/files/path", token, `{"path":"/home/user/project"}`)

	// Sync entries that should come back in relative_path ASC order
	syncBody := `{"files":[
		{"name":"z.txt","relative_path":"z.txt","is_directory":false,"size":1,"modified_ms":0},
		{"name":"a","relative_path":"a","is_directory":true,"size":0,"modified_ms":0},
		{"name":"b.txt","relative_path":"a/b.txt","is_directory":false,"size":1,"modified_ms":0},
		{"name":"c.txt","relative_path":"c.txt","is_directory":false,"size":1,"modified_ms":0}
	]}`
	authPut(t, srv.URL+"/api/files/sync", token, syncBody)

	resp := authGet(t, srv.URL+"/api/files/", token)
	var result api.WatchedFilesResponse
	decodeJSON(t, resp, &result)
	require.Len(t, result.Files, 4)

	// Expected order: "a" < "a/b.txt" < "c.txt" < "z.txt" (lexicographic relative_path ASC)
	assert.Equal(t, "a", result.Files[0].RelativePath)
	assert.Equal(t, "a/b.txt", result.Files[1].RelativePath)
	assert.Equal(t, "c.txt", result.Files[2].RelativePath)
	assert.Equal(t, "z.txt", result.Files[3].RelativePath)
}

func TestIntegration_ChangingPathClearsFiles(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	token := registerAndLogin(t, srv, "pathchange@example.com", "pass")

	// Set path and sync some files
	authPut(t, srv.URL+"/api/files/path", token, `{"path":"/old/path"}`)
	authPut(t, srv.URL+"/api/files/sync", token, `{"files":[
		{"name":"old-file.txt","relative_path":"old-file.txt","is_directory":false,"size":100,"modified_ms":0}
	]}`)

	// Verify files exist
	resp := authGet(t, srv.URL+"/api/files/", token)
	var before api.WatchedFilesResponse
	decodeJSON(t, resp, &before)
	require.Len(t, before.Files, 1)

	// Change the path
	resp = authPut(t, srv.URL+"/api/files/path", token, `{"path":"/new/path"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var wp api.WatchedPathResponse
	decodeJSON(t, resp, &wp)
	assert.Equal(t, "/new/path", wp.Path)

	// File list must now be empty — stale files cleared automatically
	resp = authGet(t, srv.URL+"/api/files/", token)
	var after api.WatchedFilesResponse
	decodeJSON(t, resp, &after)
	assert.Empty(t, after.Files, "changing the watched path must clear the stale file list")
}

func TestIntegration_FilesIsolatedPerUser(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	tokenA := registerAndLogin(t, srv, "user-a@example.com", "pass")
	tokenB := registerAndLogin(t, srv, "user-b@example.com", "pass")

	// User A sets a path and syncs a file
	authPut(t, srv.URL+"/api/files/path", tokenA, `{"path":"/a/path"}`)
	authPut(t, srv.URL+"/api/files/sync", tokenA, `{"files":[{"name":"a.txt","relative_path":"a.txt","is_directory":false,"size":1,"modified_ms":0}]}`)

	// User B has no path set
	resp := authGet(t, srv.URL+"/api/files/path", tokenB)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// User B cannot see user A's files
	resp = authGet(t, srv.URL+"/api/files/", tokenB)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
