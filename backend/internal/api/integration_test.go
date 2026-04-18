//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ali-sab/cloudbackupserver/backend/internal/api"
	"github.com/ali-sab/cloudbackupserver/backend/internal/db"
	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
	"github.com/ali-sab/cloudbackupserver/backend/internal/storage"
)

// ---- In-memory storage mock ----

// memStore is a thread-safe in-memory implementation of storage.Backend for tests.
type memStore struct {
	mu      sync.Mutex
	objects map[string][]byte // key → content
}

func newMemStore() *memStore { return &memStore{objects: make(map[string][]byte)} }

func (m *memStore) PutObject(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.objects[key] = data
	m.mu.Unlock()
	return nil
}

func (m *memStore) GetObject(_ context.Context, key string) (io.ReadCloser, int64, error) {
	m.mu.Lock()
	data, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		return nil, 0, fmt.Errorf("object %q not found", key)
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

func (m *memStore) DeleteObject(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.objects, key)
	m.mu.Unlock()
	return nil
}

func (m *memStore) DeleteUserObjects(_ context.Context, userID int64) error {
	prefix := fmt.Sprintf("%d/", userID)
	m.mu.Lock()
	for k := range m.objects {
		if strings.HasPrefix(k, prefix) {
			delete(m.objects, k)
		}
	}
	m.mu.Unlock()
	return nil
}

// Compile-time check that memStore satisfies the Backend interface.
var _ storage.Backend = (*memStore)(nil)

// setupTestEnv creates a full server backed by a real database and in-memory storage,
// and returns the server, store, and pool for tests that need direct DB access.
// TEST_DATABASE_URL must be set; otherwise the test is skipped.
// All rows inserted during the test are truncated in t.Cleanup.
func setupTestEnv(t *testing.T) (*httptest.Server, *memStore, *pgxpool.Pool) {
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
			"TRUNCATE file_backups, watched_files, watched_paths, password_reset_tokens, refresh_tokens, users RESTART IDENTITY CASCADE")
		pool.Close()
	})

	store := newMemStore()
	svc := session.NewService("integration-test-jwt-secret")
	router := api.NewRouter(pool, svc, store)
	return httptest.NewServer(router), store, pool
}

// setupTestServer creates a full test server. Use setupTestEnv when you also need
// the store or pool.
func setupTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _, _ := setupTestEnv(t)
	return srv
}

// setupTestServerWithStore is like setupTestServer but also returns the in-memory store
// so tests can inspect object storage contents directly.
func setupTestServerWithStore(t *testing.T) (*httptest.Server, *memStore) {
	t.Helper()
	srv, store, _ := setupTestEnv(t)
	return srv, store
}

// ---- helpers ----

// newTestClient returns an http.Client with a cookie jar so auth cookies are
// stored and replayed automatically across requests.
func newTestClient() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

// postJSON posts JSON using http.DefaultClient (no cookie jar).
// Use for requests that don't require an authenticated session.
func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewBufferString(body))
	require.NoError(t, err)
	return resp
}

// postJSONWithClient posts JSON using the provided client (which may have a cookie jar).
func postJSONWithClient(t *testing.T, client *http.Client, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(v))
}

// cookieValue returns the value of the named cookie from a response, or "".
func cookieValue(resp *http.Response, name string) string {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

func authGet(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func authPut(t *testing.T, client *http.Client, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func authDelete(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// postRefresh calls /api/auth/refresh via the client (cookie jar sends the token automatically).
func postRefresh(t *testing.T, client *http.Client, srvURL string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srvURL+"/api/auth/refresh", nil)
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// postRefreshWithCookie sends a raw refresh token value as a cookie, bypassing any jar.
// Used for theft-detection and expiry tests that need to replay a specific token.
func postRefreshWithCookie(t *testing.T, srvURL, rawToken string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srvURL+"/api/auth/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "refresh_token", Value: rawToken})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// postLogout calls /api/auth/logout via the client (cookie jar sends the token automatically).
func postLogout(t *testing.T, client *http.Client, srvURL string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srvURL+"/api/auth/logout", nil)
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// registerAndLogin registers a new user and returns a cookie-jar client authenticated as that user.
func registerAndLogin(t *testing.T, srv *httptest.Server, email, password string) *http.Client {
	t.Helper()
	client := newTestClient()
	resp := postJSONWithClient(t, client, srv.URL+"/api/auth/register",
		fmt.Sprintf(`{"email":%q,"password":%q}`, email, password))
	require.Equal(t, http.StatusCreated, resp.StatusCode, "registration must succeed")
	return client
}

// addFolder creates a new watched folder and returns its ID.
func addFolder(t *testing.T, client *http.Client, srvURL, path string) int64 {
	t.Helper()
	resp := postJSONWithClient(t, client, srvURL+"/api/folders",
		fmt.Sprintf(`{"path":%q}`, path))
	require.Equal(t, http.StatusCreated, resp.StatusCode, "addFolder must succeed")
	var f api.FolderResponse
	decodeJSON(t, resp, &f)
	require.NotZero(t, f.ID)
	return f.ID
}

// folderURL returns the base URL for a specific folder.
func folderURL(srvURL string, folderID int64) string {
	return fmt.Sprintf("%s/api/folders/%d", srvURL, folderID)
}

// authUpload sends a PUT /api/folders/{id}/backup/{path} with raw bytes and the required headers.
func authUpload(t *testing.T, client *http.Client, url, checksum string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Header.Set("X-Checksum-SHA256", checksum)
	req.Header.Set("X-File-Size", strconv.Itoa(len(body)))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
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

	// Tokens are now delivered as HttpOnly cookies, not in the response body.
	assert.NotEmpty(t, cookieValue(resp, "access_token"), "access_token cookie must be set")
	assert.NotEmpty(t, cookieValue(resp, "refresh_token"), "refresh_token cookie must be set")

	var auth api.AuthResponse
	decodeJSON(t, resp, &auth)
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
	assert.NotEmpty(t, cookieValue(resp, "access_token"), "access_token cookie must be set")
	assert.NotEmpty(t, cookieValue(resp, "refresh_token"), "refresh_token cookie must be set")
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

	// Register — use a cookie-jar client so the session cookie persists for the GET below.
	client := newTestClient()
	postJSONWithClient(t, client, srv.URL+"/api/auth/register",
		`{"email":"dave@example.com","password":"pass456"}`)

	// Authenticated session check — cookie jar sends the access_token cookie automatically.
	resp = authGet(t, client, srv.URL+"/api/session")
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

	// Register — cookie jar captures the initial refresh_token cookie.
	client := newTestClient()
	regResp := postJSONWithClient(t, client, srv.URL+"/api/auth/register",
		`{"email":"eve@example.com","password":"pass"}`)
	initialRefreshToken := cookieValue(regResp, "refresh_token")
	require.NotEmpty(t, initialRefreshToken)

	// Refresh → server rotates cookies; jar gets the new pair.
	refreshResp := postRefresh(t, client, srv.URL)
	assert.Equal(t, http.StatusOK, refreshResp.StatusCode)

	newRefreshToken := cookieValue(refreshResp, "refresh_token")
	assert.NotEmpty(t, newRefreshToken)
	// Refresh token must be rotated.
	assert.NotEqual(t, initialRefreshToken, newRefreshToken)

	// Old refresh token must now be rejected — bypass the jar by injecting it manually.
	resp := postRefreshWithCookie(t, srv.URL, initialRefreshToken)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestIntegration_TheftDetection(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	// Register — capture the initial refresh token before rotation.
	client := newTestClient()
	regResp := postJSONWithClient(t, client, srv.URL+"/api/auth/register",
		`{"email":"frank@example.com","password":"pass"}`)
	original := cookieValue(regResp, "refresh_token")
	require.NotEmpty(t, original)

	// Rotate once — jar now has the new token.
	postRefresh(t, client, srv.URL)

	// Re-present the already-revoked original → theft detection.
	resp := postRefreshWithCookie(t, srv.URL, original)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var errResp api.ErrorResponse
	decodeJSON(t, resp, &errResp)
	assert.Contains(t, errResp.Error, "reuse detected")
}

func TestIntegration_Logout(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := newTestClient()
	postJSONWithClient(t, client, srv.URL+"/api/auth/register",
		`{"email":"grace@example.com","password":"pass"}`)

	// Logout — server revokes the token and clears cookies in the jar.
	logoutResp := postLogout(t, client, srv.URL)
	assert.Equal(t, http.StatusNoContent, logoutResp.StatusCode)

	// Refresh after logout must fail — cookie jar has no refresh_token anymore.
	resp := postRefresh(t, client, srv.URL)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Logout again must still succeed (idempotent).
	logoutResp2 := postLogout(t, client, srv.URL)
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

	// Unknown email — dev mode returns 404 so missing accounts are easy to spot.
	resp := postJSON(t, srv.URL+"/api/auth/forgot-password", `{"email":"nobody@example.com"}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ---- Folder endpoint integration tests ----

func TestIntegration_FolderEndpoints_RequireAuth(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	for _, tc := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/folders", ""},
		{http.MethodPost, "/api/folders", `{"path":"/tmp"}`},
		{http.MethodDelete, "/api/folders/1", ""},
		{http.MethodGet, "/api/folders/1/files", ""},
		{http.MethodPut, "/api/folders/1/sync", `{"files":[]}`},
		{http.MethodGet, "/api/folders/1/backups", ""},
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

func TestIntegration_Folders_AddAndList(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "folder-user@example.com", "pass")

	// GET before any folder exists → empty list
	resp := authGet(t, client, srv.URL+"/api/folders")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var empty api.FolderStatsResponse
	decodeJSON(t, resp, &empty)
	assert.Empty(t, empty.Folders)

	// POST a folder
	resp = postJSONWithClient(t, client, srv.URL+"/api/folders", `{"path":"/home/user/documents","name":"Docs"}`)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)
	var f api.FolderResponse
	decodeJSON(t, resp, &f)
	assert.Equal(t, "/home/user/documents", f.Path)
	assert.Equal(t, "Docs", f.Name)
	assert.NotZero(t, f.ID)

	// GET now returns one folder
	resp = authGet(t, client, srv.URL+"/api/folders")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var stats api.FolderStatsResponse
	decodeJSON(t, resp, &stats)
	require.Len(t, stats.Folders, 1)
	assert.Equal(t, "/home/user/documents", stats.Folders[0].Path)
	assert.Equal(t, "Docs", stats.Folders[0].Name)
	assert.Equal(t, 0, stats.Folders[0].FileCount)
}

func TestIntegration_Folders_MultiplePerUser(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "multi-folder@example.com", "pass")

	addFolder(t, client, srv.URL, "/home/user/photos")
	addFolder(t, client, srv.URL, "/home/user/documents")

	resp := authGet(t, client, srv.URL+"/api/folders")
	var stats api.FolderStatsResponse
	decodeJSON(t, resp, &stats)
	assert.Len(t, stats.Folders, 2)
}

func TestIntegration_Folder_MissingPath(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "nopath-user@example.com", "pass")

	resp := postJSONWithClient(t, client, srv.URL+"/api/folders", `{"path":""}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestIntegration_Folder_DeleteAndVerify(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "deleter@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/home/user/docs")

	// Upload a file into the folder
	authUpload(t, client, folderURL(srv.URL, folderID)+"/backup/readme.txt", "ck1", []byte("data"))

	// Verify object is in store
	store.mu.Lock()
	countBefore := len(store.objects)
	store.mu.Unlock()
	require.Equal(t, 1, countBefore)

	// Delete the folder
	resp := authDelete(t, client, folderURL(srv.URL, folderID))
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Object storage must be empty
	store.mu.Lock()
	countAfter := len(store.objects)
	store.mu.Unlock()
	assert.Equal(t, 0, countAfter, "deleting folder must remove all backed-up objects")

	// Folder must not appear in list
	resp = authGet(t, client, srv.URL+"/api/folders")
	var stats api.FolderStatsResponse
	decodeJSON(t, resp, &stats)
	assert.Empty(t, stats.Folders)
}

func TestIntegration_Folder_DeleteNotFound(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "delnf@example.com", "pass")

	resp := authDelete(t, client, srv.URL+"/api/folders/99999")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_SyncFiles_NoFolder(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "syncnopath@example.com", "pass")

	// Sync without a valid folder ID → 404
	resp := authPut(t, client, srv.URL+"/api/folders/99999/sync", `{"files":[]}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_SyncAndGetFiles(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "sync-user@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/home/user/backups")
	base := folderURL(srv.URL, folderID)

	// GET files before any sync → empty list
	resp := authGet(t, client, base+"/files")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var empty api.WatchedFilesResponse
	decodeJSON(t, resp, &empty)
	assert.Empty(t, empty.Files)

	// Sync a set of files
	syncBody := `{"files":[
		{"name":"notes.txt","relative_path":"notes.txt","is_directory":false,"size":1024,"modified_ms":1700000000000},
		{"name":"photos","relative_path":"photos","is_directory":true,"size":0,"modified_ms":1700000001000}
	]}`
	resp = authPut(t, client, base+"/sync", syncBody)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// GET files → ordered by relative_path ASC
	resp = authGet(t, client, base+"/files")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var files api.WatchedFilesResponse
	decodeJSON(t, resp, &files)
	require.Len(t, files.Files, 2)
	assert.Equal(t, "notes.txt", files.Files[0].Name)
	assert.Equal(t, "notes.txt", files.Files[0].RelativePath)
	assert.Equal(t, int64(1024), files.Files[0].Size)
	assert.Equal(t, "photos", files.Files[1].Name)
	assert.True(t, files.Files[1].IsDirectory)

	// Re-sync with different files — must replace, not append
	resp = authPut(t, client, base+"/sync", `{"files":[
		{"name":"archive.zip","relative_path":"archive.zip","is_directory":false,"size":4096,"modified_ms":1700000002000}
	]}`)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp = authGet(t, client, base+"/files")
	var replaced api.WatchedFilesResponse
	decodeJSON(t, resp, &replaced)
	require.Len(t, replaced.Files, 1)
	assert.Equal(t, "archive.zip", replaced.Files[0].Name)
}

func TestIntegration_SyncFiles_WithRelativePath(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "relpath@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/home/user/docs")
	base := folderURL(srv.URL, folderID)

	syncBody := `{"files":[
		{"name":"readme.txt","relative_path":"readme.txt","is_directory":false,"size":512,"modified_ms":1000},
		{"name":"src","relative_path":"src","is_directory":true,"size":0,"modified_ms":2000},
		{"name":"main.go","relative_path":"src/main.go","is_directory":false,"size":2048,"modified_ms":3000}
	]}`
	resp := authPut(t, client, base+"/sync", syncBody)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp = authGet(t, client, base+"/files")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var result api.WatchedFilesResponse
	decodeJSON(t, resp, &result)
	require.Len(t, result.Files, 3)

	byPath := make(map[string]string)
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

	client := registerAndLogin(t, srv, "ordering@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/home/user/project")
	base := folderURL(srv.URL, folderID)

	syncBody := `{"files":[
		{"name":"z.txt","relative_path":"z.txt","is_directory":false,"size":1,"modified_ms":0},
		{"name":"a","relative_path":"a","is_directory":true,"size":0,"modified_ms":0},
		{"name":"b.txt","relative_path":"a/b.txt","is_directory":false,"size":1,"modified_ms":0},
		{"name":"c.txt","relative_path":"c.txt","is_directory":false,"size":1,"modified_ms":0}
	]}`
	authPut(t, client, base+"/sync", syncBody)

	resp := authGet(t, client, base+"/files")
	var result api.WatchedFilesResponse
	decodeJSON(t, resp, &result)
	require.Len(t, result.Files, 4)

	assert.Equal(t, "a", result.Files[0].RelativePath)
	assert.Equal(t, "a/b.txt", result.Files[1].RelativePath)
	assert.Equal(t, "c.txt", result.Files[2].RelativePath)
	assert.Equal(t, "z.txt", result.Files[3].RelativePath)
}

func TestIntegration_FilesIsolatedPerUser(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	clientA := registerAndLogin(t, srv, "user-a@example.com", "pass")
	clientB := registerAndLogin(t, srv, "user-b@example.com", "pass")

	folderID := addFolder(t, clientA, srv.URL, "/a/path")
	authPut(t, clientA, folderURL(srv.URL, folderID)+"/sync",
		`{"files":[{"name":"a.txt","relative_path":"a.txt","is_directory":false,"size":1,"modified_ms":0}]}`)

	// User B has no folders
	resp := authGet(t, clientB, srv.URL+"/api/folders")
	var stats api.FolderStatsResponse
	decodeJSON(t, resp, &stats)
	assert.Empty(t, stats.Folders)

	// User B cannot access user A's folder
	resp = authGet(t, clientB, folderURL(srv.URL, folderID)+"/files")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// ---- Backup endpoint integration tests ----

func TestIntegration_UploadFile_HappyPath(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "uploader@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("hello backup world")
	checksum := "abc123deadbeef"

	resp := authUpload(t, client, base+"/backup/notes.txt", checksum, content)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result api.UploadFileResponse
	decodeJSON(t, resp, &result)
	assert.Equal(t, "notes.txt", result.RelativePath)
	assert.Equal(t, int64(len(content)), result.Size)
	assert.Equal(t, checksum, result.ChecksumSHA256)
	assert.Equal(t, 1, result.Version)
	assert.False(t, result.Skipped)

	// Verify object landed in the store
	store.mu.Lock()
	keys := make([]string, 0, len(store.objects))
	for k := range store.objects {
		keys = append(keys, k)
	}
	store.mu.Unlock()
	assert.Len(t, keys, 1)
}

func TestIntegration_UploadFile_SkipsIfChecksumMatches(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "skipper@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("same content")
	checksum := "samechecksum123"

	// First upload
	resp := authUpload(t, client, base+"/backup/doc.txt", checksum, content)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Second upload — same checksum
	resp = authUpload(t, client, base+"/backup/doc.txt", checksum, content)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result api.UploadFileResponse
	decodeJSON(t, resp, &result)
	assert.True(t, result.Skipped, "second upload with same checksum must be skipped")
	assert.Equal(t, 1, result.Version, "version must not increment on a skipped upload")

	// Only one object in store (not two)
	store.mu.Lock()
	objectCount := len(store.objects)
	store.mu.Unlock()
	assert.Equal(t, 1, objectCount)
}

func TestIntegration_UploadFile_OverwritesOnChecksumChange(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "overwriter@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	// First upload
	resp := authUpload(t, client, base+"/backup/data.bin", "checksum-v1", []byte("version 1"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var r1 api.UploadFileResponse
	decodeJSON(t, resp, &r1)

	// Second upload — different checksum (file changed)
	resp = authUpload(t, client, base+"/backup/data.bin", "checksum-v2", []byte("version 2"))
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var r2 api.UploadFileResponse
	decodeJSON(t, resp, &r2)

	assert.False(t, r2.Skipped)
	assert.Equal(t, "checksum-v2", r2.ChecksumSHA256)
	assert.True(t, r2.BackedUpAt.After(r1.BackedUpAt) || r2.BackedUpAt.Equal(r1.BackedUpAt))
	assert.Equal(t, 1, r1.Version)
	assert.Equal(t, 2, r2.Version, "version must increment when file content changes")
}

func TestIntegration_DownloadFile_HappyPath(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "downloader@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("download me please")
	authUpload(t, client, base+"/backup/report.pdf", "dlchecksum", content)

	resp := authGet(t, client, base+"/backup/report.pdf")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))

	downloaded, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, content, downloaded)
}

func TestIntegration_DownloadFile_NotFound(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "nope@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	resp := authGet(t, client, base+"/backup/doesnotexist.txt")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_BackupsIsolatedPerUser(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	clientA := registerAndLogin(t, srv, "backup-a@example.com", "pass")
	clientB := registerAndLogin(t, srv, "backup-b@example.com", "pass")

	folderA := addFolder(t, clientA, srv.URL, "/a")
	folderB := addFolder(t, clientB, srv.URL, "/b")

	// User A uploads a file
	authUpload(t, clientA, folderURL(srv.URL, folderA)+"/backup/secret.txt", "ckA", []byte("user A data"))

	// User B cannot download user A's file — wrong folder ownership
	resp := authGet(t, clientB, folderURL(srv.URL, folderA)+"/backup/secret.txt")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// User B also has nothing in their own folder
	resp = authGet(t, clientB, folderURL(srv.URL, folderB)+"/backup/secret.txt")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_TwoFolders_SameRelativePath(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "twofold@example.com", "pass")
	folderA := addFolder(t, client, srv.URL, "/home/user/photos")
	folderB := addFolder(t, client, srv.URL, "/home/user/documents")

	// Same relative path in both folders
	authUpload(t, client, folderURL(srv.URL, folderA)+"/backup/README.md", "ckA", []byte("photos readme"))
	authUpload(t, client, folderURL(srv.URL, folderB)+"/backup/README.md", "ckB", []byte("docs readme"))

	// Both objects must be in store independently
	store.mu.Lock()
	count := len(store.objects)
	store.mu.Unlock()
	assert.Equal(t, 2, count, "same relative path in two folders must produce two distinct objects")

	// Downloads are independent
	respA := authGet(t, client, folderURL(srv.URL, folderA)+"/backup/README.md")
	require.Equal(t, http.StatusOK, respA.StatusCode)
	bodyA, _ := io.ReadAll(respA.Body)
	assert.Equal(t, []byte("photos readme"), bodyA)

	respB := authGet(t, client, folderURL(srv.URL, folderB)+"/backup/README.md")
	require.Equal(t, http.StatusOK, respB.StatusCode)
	bodyB, _ := io.ReadAll(respB.Body)
	assert.Equal(t, []byte("docs readme"), bodyB)
}

func TestIntegration_UploadFile_ZeroBytes(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "zerobytes@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	resp := authUpload(t, client, base+"/backup/empty.txt", "e3b0c44298fc1c149afb", []byte{})
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result api.UploadFileResponse
	decodeJSON(t, resp, &result)
	assert.Equal(t, int64(0), result.Size)
	assert.Equal(t, 1, result.Version)
	assert.False(t, result.Skipped)
}

func TestIntegration_UploadFile_EmptyPath(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "emptypath@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	resp := authUpload(t, client, base+"/backup/", "abc123", []byte("data"))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestIntegration_DownloadFile_PathTraversal(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "dltraversal@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	for _, path := range []string{"../etc/passwd", "foo/../bar"} {
		resp := authGet(t, client, base+"/backup/"+path)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "path %q should be rejected", path)
	}
}

func TestIntegration_DownloadFile_OrphanedRecord(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "orphan@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	// Upload a file to create both the DB record and the object.
	authUpload(t, client, base+"/backup/orphan.txt", "orphanck", []byte("data"))

	// Manually wipe the object store to simulate an orphaned DB record.
	store.mu.Lock()
	for k := range store.objects {
		delete(store.objects, k)
	}
	store.mu.Unlock()

	// DB record exists but the object is gone — expect 500.
	resp := authGet(t, client, base+"/backup/orphan.txt")
	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestIntegration_RefreshToken_Expired(t *testing.T) {
	srv, _, pool := setupTestEnv(t)
	defer srv.Close()

	// Register a user to get a real user ID.
	postJSON(t, srv.URL+"/api/auth/register", `{"email":"tokenexpiry@example.com","password":"pass"}`)

	var userID int64
	err := pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE email = $1`, "tokenexpiry@example.com",
	).Scan(&userID)
	require.NoError(t, err)

	// Insert a refresh token that is already expired.
	rawToken, hash, err := session.GenerateRefreshToken()
	require.NoError(t, err)
	expiredAt := time.Now().Add(-1 * time.Hour)
	require.NoError(t, db.CreateRefreshToken(context.Background(), pool, userID, hash, expiredAt))

	resp := postRefreshWithCookie(t, srv.URL, rawToken)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	var errResp api.ErrorResponse
	decodeJSON(t, resp, &errResp)
	assert.Contains(t, errResp.Error, "expired")
}

func TestIntegration_ResetToken_Expired(t *testing.T) {
	srv, _, pool := setupTestEnv(t)
	defer srv.Close()

	postJSON(t, srv.URL+"/api/auth/register", `{"email":"resetexpiry@example.com","password":"pass"}`)

	var userID int64
	err := pool.QueryRow(context.Background(),
		`SELECT id FROM users WHERE email = $1`, "resetexpiry@example.com",
	).Scan(&userID)
	require.NoError(t, err)

	// Insert a password-reset token that is already expired.
	rawToken, hash, err := session.GenerateRefreshToken() // same generation logic applies
	require.NoError(t, err)
	expiredAt := time.Now().Add(-1 * time.Hour)
	require.NoError(t, db.CreatePasswordResetToken(context.Background(), pool, userID, hash, expiredAt))

	resp := postJSON(t, srv.URL+"/api/auth/reset-password",
		fmt.Sprintf(`{"reset_token":%q,"new_password":"newpass"}`, rawToken))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var errResp api.ErrorResponse
	decodeJSON(t, resp, &errResp)
	assert.Contains(t, errResp.Error, "expired")
}
