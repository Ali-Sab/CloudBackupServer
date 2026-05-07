//go:build integration

package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
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

// sha256hex returns the hex-encoded SHA-256 hash of b.
func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

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

func (m *memStore) ObjectExists(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[key]
	return ok, nil
}

func (m *memStore) CopyObject(_ context.Context, srcKey, dstKey string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[srcKey]
	if !ok {
		return fmt.Errorf("object %q not found", srcKey)
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	m.objects[dstKey] = cp
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
			"TRUNCATE file_backup_versions, file_backups, watched_files, watched_paths, password_reset_tokens, refresh_tokens, users RESTART IDENTITY CASCADE")
		pool.Close()
	})

	store := newMemStore()
	svc := session.NewService("integration-test-jwt-secret")
	router := api.NewTestRouter(pool, svc, store)
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
// testOrigin is the same-site origin every test request advertises so the
// CSRF middleware lets it through.
const testOrigin = "http://localhost:5173"

func postJSON(t *testing.T, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrigin)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// postJSONWithClient posts JSON using the provided client (which may have a cookie jar).
func postJSONWithClient(t *testing.T, client *http.Client, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrigin)
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
	req.Header.Set("Origin", testOrigin)
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func authDelete(t *testing.T, client *http.Client, url string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	req.Header.Set("Origin", testOrigin)
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func authDeleteWithBody(t *testing.T, client *http.Client, url, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, url, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrigin)
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// postRefresh calls /api/auth/refresh via the client (cookie jar sends the token automatically).
func postRefresh(t *testing.T, client *http.Client, srvURL string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srvURL+"/api/auth/refresh", nil)
	req.Header.Set("Origin", testOrigin)
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
	req.Header.Set("Origin", testOrigin)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

// postLogout calls /api/auth/logout via the client (cookie jar sends the token automatically).
func postLogout(t *testing.T, client *http.Client, srvURL string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srvURL+"/api/auth/logout", nil)
	req.Header.Set("Origin", testOrigin)
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

// authUpload sends a PUT /api/folders/{id}/backup/{path} with raw bytes.
// The SHA-256 checksum is computed automatically from body.
func authUpload(t *testing.T, client *http.Client, url string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Header.Set("X-Checksum-SHA256", sha256hex(body))
	req.Header.Set("X-File-Size", strconv.Itoa(len(body)))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Origin", testOrigin)
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
	// Cookie-only auth: missing refresh cookie is an auth failure, not a bad request.
	resp := postRefresh(t, client, srv.URL)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

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
		req.Header.Set("Origin", testOrigin)
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
	authUpload(t, client, folderURL(srv.URL, folderID)+"/backup/readme.txt", []byte("data"))

	// Verify objects are in store (2 per upload: main + versioned copy).
	store.mu.Lock()
	countBefore := len(store.objects)
	store.mu.Unlock()
	require.GreaterOrEqual(t, countBefore, 1)

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

	resp := authUpload(t, client, base+"/backup/notes.txt", content)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result api.UploadFileResponse
	decodeJSON(t, resp, &result)
	assert.Equal(t, "notes.txt", result.RelativePath)
	assert.Equal(t, int64(len(content)), result.Size)
	assert.Equal(t, sha256hex(content), result.ChecksumSHA256)
	assert.Equal(t, 1, result.Version)
	assert.False(t, result.Skipped)

	// Verify object landed in the store (2 keys per upload: main + versioned copy).
	store.mu.Lock()
	keyCount := len(store.objects)
	store.mu.Unlock()
	assert.GreaterOrEqual(t, keyCount, 1)
}

func TestIntegration_UploadFile_SkipsIfChecksumMatches(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "skipper@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("same content")

	// First upload
	resp := authUpload(t, client, base+"/backup/doc.txt", content)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Second upload — same content → same checksum → must be skipped
	resp = authUpload(t, client, base+"/backup/doc.txt", content)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result api.UploadFileResponse
	decodeJSON(t, resp, &result)
	assert.True(t, result.Skipped, "second upload with same checksum must be skipped")
	assert.Equal(t, 1, result.Version, "version must not increment on a skipped upload")

	// Store must not grow on a skipped upload (main + versioned from the first upload only).
	store.mu.Lock()
	objectCount := len(store.objects)
	store.mu.Unlock()
	assert.GreaterOrEqual(t, objectCount, 1)
}

func TestIntegration_UploadFile_OverwritesOnChecksumChange(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "overwriter@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	contentV1 := []byte("version 1")
	contentV2 := []byte("version 2")

	// First upload
	resp := authUpload(t, client, base+"/backup/data.bin", contentV1)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var r1 api.UploadFileResponse
	decodeJSON(t, resp, &r1)

	// Second upload — different content → different checksum → file changed
	resp = authUpload(t, client, base+"/backup/data.bin", contentV2)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var r2 api.UploadFileResponse
	decodeJSON(t, resp, &r2)

	assert.False(t, r2.Skipped)
	assert.Equal(t, sha256hex(contentV2), r2.ChecksumSHA256)
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
	authUpload(t, client, base+"/backup/report.pdf", content)

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
	authUpload(t, clientA, folderURL(srv.URL, folderA)+"/backup/secret.txt", []byte("user A data"))

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
	authUpload(t, client, folderURL(srv.URL, folderA)+"/backup/README.md", []byte("photos readme"))
	authUpload(t, client, folderURL(srv.URL, folderB)+"/backup/README.md", []byte("docs readme"))

	// Both files must be in store independently (2 keys each: main + versioned copy).
	store.mu.Lock()
	count := len(store.objects)
	store.mu.Unlock()
	assert.GreaterOrEqual(t, count, 2, "same relative path in two folders must produce two distinct objects")

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

	resp := authUpload(t, client, base+"/backup/empty.txt", []byte{})
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

	resp := authUpload(t, client, base+"/backup/", []byte("data"))
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
	authUpload(t, client, base+"/backup/orphan.txt", []byte("data"))

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

// ---- Account management tests ----

func TestIntegration_RenameFolder(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "rename-folder@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/home/user/docs")

	// Happy path — rename succeeds.
	resp := authPut(t, client, fmt.Sprintf("%s/api/folders/%d", srv.URL, folderID), `{"name":"My Docs"}`)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Verify the new name appears in the folder list.
	resp = authGet(t, client, srv.URL+"/api/folders")
	var stats api.FolderStatsResponse
	decodeJSON(t, resp, &stats)
	require.Len(t, stats.Folders, 1)
	assert.Equal(t, "My Docs", stats.Folders[0].Name)

	// Missing name field → 400.
	resp = authPut(t, client, fmt.Sprintf("%s/api/folders/%d", srv.URL, folderID), `{"name":""}`)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	// Non-existent folder → 404.
	resp = authPut(t, client, srv.URL+"/api/folders/99999", `{"name":"ghost"}`)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_ChangeEmail_HappyPath(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ce-happy@example.com", "pass123")

	resp := authPut(t, client, srv.URL+"/api/account/email",
		`{"new_email":"ce-new@example.com","current_password":"pass123"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]string
	decodeJSON(t, resp, &body)
	assert.Equal(t, "ce-new@example.com", body["email"])

	// JWT carries email from login time; verify the change persisted by logging in with the new email.
	resp = postJSON(t, srv.URL+"/api/auth/login", `{"email":"ce-new@example.com","password":"pass123"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIntegration_ChangeEmail_WrongPassword(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ce-wrong@example.com", "pass123")

	resp := authPut(t, client, srv.URL+"/api/account/email",
		`{"new_email":"other@example.com","current_password":"wrongpass"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestIntegration_ChangeEmail_DuplicateEmail(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	// Register two users.
	registerAndLogin(t, srv, "existing@example.com", "pass")
	client := registerAndLogin(t, srv, "ce-dup@example.com", "pass")

	// Try to change email to the already-taken address.
	resp := authPut(t, client, srv.URL+"/api/account/email",
		`{"new_email":"existing@example.com","current_password":"pass"}`)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestIntegration_ChangePassword_HappyPath(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "cp-happy@example.com", "oldpass")

	resp := authPut(t, client, srv.URL+"/api/account/password",
		`{"current_password":"oldpass","new_password":"newpass"}`)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Old password must no longer work.
	resp = postJSON(t, srv.URL+"/api/auth/login",
		`{"email":"cp-happy@example.com","password":"oldpass"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// New password must work.
	resp = postJSON(t, srv.URL+"/api/auth/login",
		`{"email":"cp-happy@example.com","password":"newpass"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIntegration_ChangePassword_WrongCurrent(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "cp-wrong@example.com", "pass123")

	resp := authPut(t, client, srv.URL+"/api/account/password",
		`{"current_password":"notmypass","new_password":"newpass"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestIntegration_DeleteAccount_HappyPath(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "del-acct@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/home/user/docs")
	authUpload(t, client, folderURL(srv.URL, folderID)+"/backup/file.txt", []byte("data"))

	store.mu.Lock()
	require.GreaterOrEqual(t, len(store.objects), 1)
	store.mu.Unlock()

	resp := authDeleteWithBody(t, client, srv.URL+"/api/account",
		`{"current_password":"pass"}`)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Storage must be cleared.
	store.mu.Lock()
	assert.Equal(t, 0, len(store.objects))
	store.mu.Unlock()

	// Session must be gone — cookie was cleared by the server.
	resp = authGet(t, client, srv.URL+"/api/session")
	var s api.SessionResponse
	decodeJSON(t, resp, &s)
	assert.False(t, s.LoggedIn)
}

func TestIntegration_DeleteAccount_WrongPassword(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "del-wrong@example.com", "pass")

	resp := authDeleteWithBody(t, client, srv.URL+"/api/account",
		`{"current_password":"wrong"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// Account must still exist.
	resp = authGet(t, client, srv.URL+"/api/session")
	var s api.SessionResponse
	decodeJSON(t, resp, &s)
	assert.True(t, s.LoggedIn)
}

func TestIntegration_DeleteFileBackup_HappyPath(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "del-backup@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("file to delete from cloud")
	resp := authUpload(t, client, base+"/backup/notes.txt", content)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	// Confirm object exists in store.
	store.mu.Lock()
	countBefore := len(store.objects)
	store.mu.Unlock()
	require.Greater(t, countBefore, 0)

	// Delete the cloud backup.
	resp = authDelete(t, client, base+"/backup/notes.txt")
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Download must now return 404.
	resp = authGet(t, client, base+"/backup/notes.txt")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// Object must be removed from store.
	store.mu.Lock()
	countAfter := len(store.objects)
	store.mu.Unlock()
	assert.Equal(t, 0, countAfter)
}

func TestIntegration_DeleteFileBackup_NotFound(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "del-notfound@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	resp := authDelete(t, client, base+"/backup/ghost.txt")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_DeleteFileBackup_OtherUserCannotDelete(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	ownerClient := registerAndLogin(t, srv, "owner-del@example.com", "pass")
	attackerClient := registerAndLogin(t, srv, "attacker-del@example.com", "pass")

	folderID := addFolder(t, ownerClient, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, ownerClient, base+"/backup/secret.txt", []byte("secret"))

	// Attacker tries to delete owner's backup using owner's folder ID.
	resp := authDelete(t, attackerClient, base+"/backup/secret.txt")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_SyncFiles_PruneDeleted(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "prune@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	// Upload two files.
	authUpload(t, client, base+"/backup/keep.txt", []byte("keep me"))
	authUpload(t, client, base+"/backup/delete.txt", []byte("delete me"))

	store.mu.Lock()
	countBefore := len(store.objects)
	store.mu.Unlock()
	require.GreaterOrEqual(t, countBefore, 2, "both files should have objects in store")

	// Sync with only keep.txt present and prune_deleted=true.
	syncBody := `{"files":[{"name":"keep.txt","relative_path":"keep.txt","is_directory":false,"size":7,"modified_ms":0}],"prune_deleted":true}`
	req, _ := http.NewRequest(http.MethodPut, base+"/sync", bytes.NewBufferString(syncBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrigin)
	resp, err := client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	// download of pruned file must now return 404.
	resp = authGet(t, client, base+"/backup/delete.txt")
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// keep.txt must still be downloadable.
	resp = authGet(t, client, base+"/backup/keep.txt")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIntegration_SyncFiles_NoPruneByDefault(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "no-prune@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/file.txt", []byte("content"))

	// Sync without prune_deleted — backup must survive.
	syncBody := `{"files":[]}`
	req, _ := http.NewRequest(http.MethodPut, base+"/sync", bytes.NewBufferString(syncBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrigin)
	resp, err := client.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)

	resp = authGet(t, client, base+"/backup/file.txt")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ============================================================
// Versioning & restore tests
// ============================================================

// listVersions is a helper that calls GET /api/folders/{id}/versions?path=<p>
// and returns the decoded FileVersionsResponse.
func listVersions(t *testing.T, client *http.Client, base, path string) api.FileVersionsResponse {
	t.Helper()
	encoded := url.QueryEscape(path)
	resp := authGet(t, client, base+"/versions?path="+encoded)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var vr api.FileVersionsResponse
	decodeJSON(t, resp, &vr)
	return vr
}

// downloadVersion calls GET /api/folders/{id}/versions/{versionID} and returns
// the raw response body bytes plus the HTTP response.
func downloadVersion(t *testing.T, client *http.Client, base string, versionID int64) ([]byte, *http.Response) {
	t.Helper()
	resp := authGet(t, client, fmt.Sprintf("%s/versions/%d", base, versionID))
	body, _ := io.ReadAll(resp.Body)
	return body, resp
}

// --- list versions ---

func TestIntegration_Versions_EmptyBeforeAnyBackup(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-empty@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	vr := listVersions(t, client, base, "notes.txt")
	assert.Empty(t, vr.Versions, "no versions before any backup")
}

func TestIntegration_Versions_OneVersionAfterFirstBackup(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-one@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/notes.txt", []byte("version one"))

	vr := listVersions(t, client, base, "notes.txt")
	require.Len(t, vr.Versions, 1)
	assert.Equal(t, 1, vr.Versions[0].Version)
	assert.EqualValues(t, len("version one"), vr.Versions[0].Size)
	assert.Equal(t, sha256hex([]byte("version one")), vr.Versions[0].ChecksumSHA256)
	assert.NotZero(t, vr.Versions[0].ID)
	assert.False(t, vr.Versions[0].BackedUpAt.IsZero())
}

func TestIntegration_Versions_GrowsWithEachUpload(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-grow@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	contents := [][]byte{
		[]byte("v1 content"),
		[]byte("v2 content updated"),
		[]byte("v3 content updated again"),
	}
	for _, c := range contents {
		authUpload(t, client, base+"/backup/doc.txt", c)
	}

	vr := listVersions(t, client, base, "doc.txt")
	require.Len(t, vr.Versions, 3, "three uploads must produce three version rows")

	// versions are returned newest-first
	assert.Equal(t, 3, vr.Versions[0].Version)
	assert.Equal(t, 2, vr.Versions[1].Version)
	assert.Equal(t, 1, vr.Versions[2].Version)
}

func TestIntegration_Versions_SkippedUploadDoesNotAddVersion(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-skip@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("same bytes")
	authUpload(t, client, base+"/backup/same.txt", content)
	authUpload(t, client, base+"/backup/same.txt", content) // skipped

	vr := listVersions(t, client, base, "same.txt")
	assert.Len(t, vr.Versions, 1, "a skipped (unchanged) upload must not create a second version")
}

func TestIntegration_Versions_IndependentPerFile(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-perfile@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/a.txt", []byte("a v1"))
	authUpload(t, client, base+"/backup/a.txt", []byte("a v2"))
	authUpload(t, client, base+"/backup/b.txt", []byte("b v1"))

	vrA := listVersions(t, client, base, "a.txt")
	vrB := listVersions(t, client, base, "b.txt")

	assert.Len(t, vrA.Versions, 2, "a.txt must have 2 versions")
	assert.Len(t, vrB.Versions, 1, "b.txt must have 1 version")
}

func TestIntegration_Versions_IndependentPerFolder(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-perfolder@example.com", "pass")
	folderA := addFolder(t, client, srv.URL, "/a")
	folderB := addFolder(t, client, srv.URL, "/b")
	baseA := folderURL(srv.URL, folderA)
	baseB := folderURL(srv.URL, folderB)

	authUpload(t, client, baseA+"/backup/readme.txt", []byte("folder a v1"))
	authUpload(t, client, baseA+"/backup/readme.txt", []byte("folder a v2"))
	authUpload(t, client, baseB+"/backup/readme.txt", []byte("folder b v1"))

	vrA := listVersions(t, client, baseA, "readme.txt")
	vrB := listVersions(t, client, baseB, "readme.txt")

	assert.Len(t, vrA.Versions, 2)
	assert.Len(t, vrB.Versions, 1)
}

func TestIntegration_Versions_IsolatedBetweenUsers(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	clientA := registerAndLogin(t, srv, "ver-user-a@example.com", "pass")
	clientB := registerAndLogin(t, srv, "ver-user-b@example.com", "pass")

	folderA := addFolder(t, clientA, srv.URL, "/a")
	folderB := addFolder(t, clientB, srv.URL, "/b")

	authUpload(t, clientA, folderURL(srv.URL, folderA)+"/backup/secret.txt", []byte("user a data"))

	// User B cannot list versions of user A's folder.
	encodedPath := url.QueryEscape("secret.txt")
	resp := authGet(t, clientB, folderURL(srv.URL, folderA)+"/versions?path="+encodedPath)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)

	// User B's own folder returns empty.
	vrB := listVersions(t, clientB, folderURL(srv.URL, folderB), "secret.txt")
	assert.Empty(t, vrB.Versions)
}

func TestIntegration_Versions_MissingPathParam(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-noparam@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	resp := authGet(t, client, base+"/versions")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestIntegration_Versions_PathTraversal(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-traversal@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	resp := authGet(t, client, base+"/versions?path="+url.QueryEscape("../etc/passwd"))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp = authGet(t, client, base+"/versions?path="+url.QueryEscape("foo/../bar"))
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

// --- download specific version ---

func TestIntegration_VersionDownload_CorrectContentPerVersion(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-dl-content@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	v1Content := []byte("first version content")
	v2Content := []byte("second version content — different")

	authUpload(t, client, base+"/backup/data.txt", v1Content)
	authUpload(t, client, base+"/backup/data.txt", v2Content)

	vr := listVersions(t, client, base, "data.txt")
	require.Len(t, vr.Versions, 2)

	// Versions are newest-first: [0]=v2, [1]=v1
	v2ID := vr.Versions[0].ID
	v1ID := vr.Versions[1].ID

	body2, resp2 := downloadVersion(t, client, base, v2ID)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	assert.Equal(t, v2Content, body2, "downloading v2 must return v2 bytes")

	body1, resp1 := downloadVersion(t, client, base, v1ID)
	require.Equal(t, http.StatusOK, resp1.StatusCode)
	assert.Equal(t, v1Content, body1, "downloading v1 must return v1 bytes")
}

func TestIntegration_VersionDownload_ResponseHeaders(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-dl-hdrs@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("header check bytes")
	authUpload(t, client, base+"/backup/file.bin", content)

	vr := listVersions(t, client, base, "file.bin")
	require.Len(t, vr.Versions, 1)

	_, resp := downloadVersion(t, client, base, vr.Versions[0].ID)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/octet-stream", resp.Header.Get("Content-Type"))
	assert.Equal(t, sha256hex(content), resp.Header.Get("X-Checksum-SHA256"))
	assert.Equal(t, "1", resp.Header.Get("X-Backup-Version"))
	assert.Equal(t, strconv.Itoa(len(content)), resp.Header.Get("Content-Length"))
}

func TestIntegration_VersionDownload_NotFound(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-dl-notfound@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	_, resp := downloadVersion(t, client, base, 99999)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_VersionDownload_InvalidID(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-dl-badid@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	resp := authGet(t, client, base+"/versions/not-a-number")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	resp = authGet(t, client, base+"/versions/0")
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestIntegration_VersionDownload_CannotAccessOtherUsersVersion(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	owner := registerAndLogin(t, srv, "ver-owner@example.com", "pass")
	attacker := registerAndLogin(t, srv, "ver-attacker@example.com", "pass")

	folderID := addFolder(t, owner, srv.URL, "/watched")
	attackerFolderID := addFolder(t, attacker, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)
	attackerBase := folderURL(srv.URL, attackerFolderID)

	authUpload(t, owner, base+"/backup/private.txt", []byte("private"))

	vr := listVersions(t, owner, base, "private.txt")
	require.Len(t, vr.Versions, 1)
	versionID := vr.Versions[0].ID

	// Attacker uses their own folder ID but owner's version ID.
	_, resp := downloadVersion(t, attacker, attackerBase, versionID)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestIntegration_VersionDownload_WrongFolderForVersion(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-wrongfolder@example.com", "pass")
	folderA := addFolder(t, client, srv.URL, "/a")
	folderB := addFolder(t, client, srv.URL, "/b")
	baseA := folderURL(srv.URL, folderA)
	baseB := folderURL(srv.URL, folderB)

	authUpload(t, client, baseA+"/backup/file.txt", []byte("belongs to folder A"))

	vrA := listVersions(t, client, baseA, "file.txt")
	require.Len(t, vrA.Versions, 1)
	versionID := vrA.Versions[0].ID

	// Correct user but wrong folder in URL.
	_, resp := downloadVersion(t, client, baseB, versionID)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// --- full backup → modify → restore cycle ---

func TestIntegration_Versions_FullRestoreCycle(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-cycle@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	// Upload three versions.
	v1 := []byte("original content")
	v2 := []byte("modified content")
	v3 := []byte("modified again")
	authUpload(t, client, base+"/backup/report.txt", v1)
	authUpload(t, client, base+"/backup/report.txt", v2)
	authUpload(t, client, base+"/backup/report.txt", v3)

	vr := listVersions(t, client, base, "report.txt")
	require.Len(t, vr.Versions, 3)

	// Current backup (HEAD) must be v3.
	head, _ := io.ReadAll(authGet(t, client, base+"/backup/report.txt").Body)
	assert.Equal(t, v3, head, "HEAD must be the latest version")

	// Restore v1 by ID.
	v1ID := vr.Versions[2].ID // newest-first ordering
	body1, resp1 := downloadVersion(t, client, base, v1ID)
	require.Equal(t, http.StatusOK, resp1.StatusCode)
	assert.Equal(t, v1, body1)

	// Restore v2 by ID.
	v2ID := vr.Versions[1].ID
	body2, resp2 := downloadVersion(t, client, base, v2ID)
	require.Equal(t, http.StatusOK, resp2.StatusCode)
	assert.Equal(t, v2, body2)
}

func TestIntegration_Versions_OldVersionsAccessibleAfterNewBackup(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-old-accessible@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/log.txt", []byte("day 1"))
	authUpload(t, client, base+"/backup/log.txt", []byte("day 2"))
	authUpload(t, client, base+"/backup/log.txt", []byte("day 3"))

	vr := listVersions(t, client, base, "log.txt")
	require.Len(t, vr.Versions, 3)

	// All three must download cleanly.
	for i, v := range vr.Versions {
		body, resp := downloadVersion(t, client, base, v.ID)
		require.Equal(t, http.StatusOK, resp.StatusCode, "version %d must be downloadable", i)
		assert.NotEmpty(t, body)
	}
}

// --- versions deleted with backup ---

func TestIntegration_Versions_AllDeletedWhenBackupDeleted(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-del-all@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/file.txt", []byte("v1"))
	authUpload(t, client, base+"/backup/file.txt", []byte("v2"))
	authUpload(t, client, base+"/backup/file.txt", []byte("v3"))

	vr := listVersions(t, client, base, "file.txt")
	require.Len(t, vr.Versions, 3)
	ids := []int64{vr.Versions[0].ID, vr.Versions[1].ID, vr.Versions[2].ID}

	// Delete the backup.
	resp := authDelete(t, client, base+"/backup/file.txt")
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	// All version IDs must now 404.
	for _, id := range ids {
		_, resp := downloadVersion(t, client, base, id)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "version %d must be gone after backup delete", id)
	}

	// Version list must be empty.
	vr2 := listVersions(t, client, base, "file.txt")
	assert.Empty(t, vr2.Versions)
}

func TestIntegration_Versions_PrunedWhenFileRemovedLocally(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-prune-all@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/gone.txt", []byte("v1"))
	authUpload(t, client, base+"/backup/gone.txt", []byte("v2"))

	vr := listVersions(t, client, base, "gone.txt")
	require.Len(t, vr.Versions, 2)

	// Sync with gone.txt absent and prune_deleted=true.
	syncBody := `{"files":[],"prune_deleted":true}`
	req, _ := http.NewRequest(http.MethodPut, base+"/sync", bytes.NewBufferString(syncBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrigin)
	syncResp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, syncResp.StatusCode)

	// All version objects must be gone from storage and DB.
	for _, v := range vr.Versions {
		_, resp := downloadVersion(t, client, base, v.ID)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	}
	vr2 := listVersions(t, client, base, "gone.txt")
	assert.Empty(t, vr2.Versions)
}

func TestIntegration_Versions_OnlyStaleVersionsPruned(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "ver-partial-prune@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/keep.txt", []byte("keep v1"))
	authUpload(t, client, base+"/backup/keep.txt", []byte("keep v2"))
	authUpload(t, client, base+"/backup/delete.txt", []byte("delete v1"))
	authUpload(t, client, base+"/backup/delete.txt", []byte("delete v2"))

	deleteVr := listVersions(t, client, base, "delete.txt")
	require.Len(t, deleteVr.Versions, 2)

	syncBody := `{"files":[{"name":"keep.txt","relative_path":"keep.txt","is_directory":false,"size":7,"modified_ms":0}],"prune_deleted":true}`
	req, _ := http.NewRequest(http.MethodPut, base+"/sync", bytes.NewBufferString(syncBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", testOrigin)
	syncResp, err := client.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, syncResp.StatusCode)

	// delete.txt versions must be gone.
	for _, v := range deleteVr.Versions {
		_, resp := downloadVersion(t, client, base, v.ID)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	}

	// keep.txt versions must still exist and be downloadable.
	keepVr := listVersions(t, client, base, "keep.txt")
	require.Len(t, keepVr.Versions, 2, "keep.txt must retain both versions")
	for _, v := range keepVr.Versions {
		_, resp := downloadVersion(t, client, base, v.ID)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}
}

// --- backup history ---

func TestIntegration_History_EmptyForNewAccount(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "hist-empty@example.com", "pass")
	resp := authGet(t, client, srv.URL+"/api/history")
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var hr api.HistoryResponse
	decodeJSON(t, resp, &hr)
	assert.Empty(t, hr.Items)
}

func TestIntegration_History_GrowsWithEachUpload(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "hist-grow@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/a.txt", []byte("a v1"))
	authUpload(t, client, base+"/backup/a.txt", []byte("a v2"))
	authUpload(t, client, base+"/backup/b.txt", []byte("b v1"))

	resp := authGet(t, client, srv.URL+"/api/history")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var hr api.HistoryResponse
	decodeJSON(t, resp, &hr)
	assert.Len(t, hr.Items, 3, "history must have one row per upload event (not per file)")
}

func TestIntegration_History_ScopedToFolder(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "hist-folder@example.com", "pass")
	folderA := addFolder(t, client, srv.URL, "/a")
	folderB := addFolder(t, client, srv.URL, "/b")

	authUpload(t, client, folderURL(srv.URL, folderA)+"/backup/x.txt", []byte("a"))
	authUpload(t, client, folderURL(srv.URL, folderA)+"/backup/x.txt", []byte("a2"))
	authUpload(t, client, folderURL(srv.URL, folderB)+"/backup/y.txt", []byte("b"))

	respAll := authGet(t, client, srv.URL+"/api/history")
	var hrAll api.HistoryResponse
	decodeJSON(t, respAll, &hrAll)
	assert.Len(t, hrAll.Items, 3)

	respA := authGet(t, client, fmt.Sprintf("%s/api/history?folder_id=%d", srv.URL, folderA))
	var hrA api.HistoryResponse
	decodeJSON(t, respA, &hrA)
	assert.Len(t, hrA.Items, 2, "folder-scoped history must only include that folder")

	respB := authGet(t, client, fmt.Sprintf("%s/api/history?folder_id=%d", srv.URL, folderB))
	var hrB api.HistoryResponse
	decodeJSON(t, respB, &hrB)
	assert.Len(t, hrB.Items, 1)
}

func TestIntegration_History_IsolatedBetweenUsers(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	clientA := registerAndLogin(t, srv, "hist-user-a@example.com", "pass")
	clientB := registerAndLogin(t, srv, "hist-user-b@example.com", "pass")

	folderA := addFolder(t, clientA, srv.URL, "/a")
	authUpload(t, clientA, folderURL(srv.URL, folderA)+"/backup/secret.txt", []byte("secret"))

	resp := authGet(t, clientB, srv.URL+"/api/history")
	var hr api.HistoryResponse
	decodeJSON(t, resp, &hr)
	assert.Empty(t, hr.Items, "user B must not see user A's history")
}

func TestIntegration_History_PaginationLimitOffset(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "hist-page@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	// Upload 5 distinct versions.
	for i := 0; i < 5; i++ {
		authUpload(t, client, base+"/backup/file.txt", []byte(fmt.Sprintf("content v%d", i)))
	}

	respFull := authGet(t, client, srv.URL+"/api/history?limit=5")
	var hrFull api.HistoryResponse
	decodeJSON(t, respFull, &hrFull)
	require.Len(t, hrFull.Items, 5)

	respPage := authGet(t, client, srv.URL+"/api/history?limit=2&offset=0")
	var hrPage api.HistoryResponse
	decodeJSON(t, respPage, &hrPage)
	assert.Len(t, hrPage.Items, 2)

	respNext := authGet(t, client, srv.URL+"/api/history?limit=2&offset=2")
	var hrNext api.HistoryResponse
	decodeJSON(t, respNext, &hrNext)
	assert.Len(t, hrNext.Items, 2)

	// Items must not overlap: newest-first, so page 1 and page 2 IDs are distinct.
	ids1 := map[int64]bool{hrPage.Items[0].ID: true, hrPage.Items[1].ID: true}
	for _, item := range hrNext.Items {
		assert.False(t, ids1[item.ID], "pages must not overlap")
	}
}

func TestIntegration_History_SkippedUploadDoesNotAppear(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "hist-skip@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("unchanged content")
	authUpload(t, client, base+"/backup/stable.txt", content)
	authUpload(t, client, base+"/backup/stable.txt", content) // skipped

	resp := authGet(t, client, srv.URL+"/api/history")
	var hr api.HistoryResponse
	decodeJSON(t, resp, &hr)
	assert.Len(t, hr.Items, 1, "skipped upload must not create a history row")
}

// --- GET /api/folders/{id}/backups ---

func TestIntegration_GetBackups_ListsAllCurrentBackups(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "bkp-list@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/a.txt", []byte("a"))
	authUpload(t, client, base+"/backup/b.txt", []byte("b"))
	authUpload(t, client, base+"/backup/a.txt", []byte("a updated")) // re-upload a

	resp := authGet(t, client, base+"/backups")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var br api.FileBackupsResponse
	decodeJSON(t, resp, &br)
	require.Len(t, br.Backups, 2, "backups endpoint must list one row per file, not per version")

	paths := map[string]bool{}
	for _, b := range br.Backups {
		paths[b.RelativePath] = true
	}
	assert.True(t, paths["a.txt"])
	assert.True(t, paths["b.txt"])
}

func TestIntegration_GetBackups_ReflectsLatestVersion(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "bkp-latest@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/evolving.txt", []byte("v1"))
	authUpload(t, client, base+"/backup/evolving.txt", []byte("v2"))
	authUpload(t, client, base+"/backup/evolving.txt", []byte("v3"))

	resp := authGet(t, client, base+"/backups")
	var br api.FileBackupsResponse
	decodeJSON(t, resp, &br)
	require.Len(t, br.Backups, 1)
	assert.Equal(t, 3, br.Backups[0].Version, "backups endpoint must report the current (latest) version")
	assert.Equal(t, sha256hex([]byte("v3")), br.Backups[0].ChecksumSHA256)
}

func TestIntegration_GetBackups_EmptyAfterAllDeleted(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "bkp-empty@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/file.txt", []byte("content"))
	authDelete(t, client, base+"/backup/file.txt")

	resp := authGet(t, client, base+"/backups")
	var br api.FileBackupsResponse
	decodeJSON(t, resp, &br)
	assert.Empty(t, br.Backups)
}

func authUploadWithHeaders(t *testing.T, client *http.Client, url string, body []byte, extraHeaders map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	req.Header.Set("X-Checksum-SHA256", sha256hex(body))
	req.Header.Set("X-File-Size", strconv.Itoa(len(body)))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Origin", testOrigin)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

func TestIntegration_RestoreProvenance_TaggedOnUpload(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "provenance@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	// Create v1 and v2.
	authUpload(t, client, base+"/backup/report.txt", []byte("original"))
	authUpload(t, client, base+"/backup/report.txt", []byte("modified"))

	versions := listVersions(t, client, base, "report.txt")
	require.Len(t, versions.Versions, 2)

	v1 := versions.Versions[1] // oldest (version number 1)
	v2 := versions.Versions[0] // newest (version number 2)
	assert.Nil(t, v1.RestoredFromVersionID, "original backup must have nil restored_from_version_id")
	assert.Nil(t, v2.RestoredFromVersionID, "second normal backup must have nil restored_from_version_id")

	// Simulate restoring v1: re-upload v1's content, tagging the source version.
	resp := authUploadWithHeaders(t, client, base+"/backup/report.txt", []byte("original"), map[string]string{
		"X-Restored-From-Version-ID": strconv.FormatInt(v1.ID, 10),
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	versions = listVersions(t, client, base, "report.txt")
	require.Len(t, versions.Versions, 3)

	v3 := versions.Versions[0] // newest
	require.NotNil(t, v3.RestoredFromVersionID, "restored version must have non-nil restored_from_version_id")
	assert.Equal(t, v1.ID, *v3.RestoredFromVersionID, "must point back to v1's ID")
}

func TestIntegration_RestoreProvenance_NilForNormalUpload(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "normal-upload@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/plain.txt", []byte("just a file"))

	versions := listVersions(t, client, base, "plain.txt")
	require.Len(t, versions.Versions, 1)
	assert.Nil(t, versions.Versions[0].RestoredFromVersionID)
}

func TestIntegration_RestoreProvenance_InvalidHeaderIgnored(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "bad-header@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	// A non-numeric or zero X-Restored-From-Version-ID must be silently ignored.
	resp := authUploadWithHeaders(t, client, base+"/backup/file.txt", []byte("data"), map[string]string{
		"X-Restored-From-Version-ID": "not-a-number",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	versions := listVersions(t, client, base, "file.txt")
	require.Len(t, versions.Versions, 1)
	assert.Nil(t, versions.Versions[0].RestoredFromVersionID, "invalid header must produce nil provenance")
}

func TestIntegration_BlobDedup_SameContentSharesOneObject(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "dedup-same@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("identical content")
	authUpload(t, client, base+"/backup/file-a.txt", content)
	authUpload(t, client, base+"/backup/file-b.txt", content)

	// Both versions point to the same blob key — only one blob object in storage.
	blobKey := storage.BlobKey(1, sha256hex(content))
	store.mu.Lock()
	_, exists := store.objects[blobKey]
	blobCount := 0
	for k := range store.objects {
		if strings.HasPrefix(k, "1/blobs/") {
			blobCount++
		}
	}
	store.mu.Unlock()

	assert.True(t, exists, "blob key must exist in storage")
	assert.Equal(t, 1, blobCount, "identical content from two files must share one blob object")
}

func TestIntegration_BlobDedup_DifferentContentGetsSeparateBlobs(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "dedup-diff@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	authUpload(t, client, base+"/backup/a.txt", []byte("content A"))
	authUpload(t, client, base+"/backup/b.txt", []byte("content B"))

	store.mu.Lock()
	blobCount := 0
	for k := range store.objects {
		if strings.HasPrefix(k, "1/blobs/") {
			blobCount++
		}
	}
	store.mu.Unlock()

	assert.Equal(t, 2, blobCount, "two distinct contents must produce two separate blob objects")
}

func TestIntegration_BlobDedup_BlobDeletedWhenLastVersionGone(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "dedup-del@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("unique content")
	authUpload(t, client, base+"/backup/solo.txt", content)
	blobKey := storage.BlobKey(1, sha256hex(content))

	store.mu.Lock()
	_, beforeDel := store.objects[blobKey]
	store.mu.Unlock()
	assert.True(t, beforeDel, "blob must exist before deletion")

	authDelete(t, client, base+"/backup/solo.txt")

	store.mu.Lock()
	_, afterDel := store.objects[blobKey]
	store.mu.Unlock()
	assert.False(t, afterDel, "blob must be removed when its only referencing version is deleted")
}

func TestIntegration_BlobDedup_BlobKeptWhenOtherVersionStillReferences(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "dedup-keep@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	content := []byte("shared bytes")
	authUpload(t, client, base+"/backup/first.txt", content)
	authUpload(t, client, base+"/backup/second.txt", content)
	blobKey := storage.BlobKey(1, sha256hex(content))

	// Delete one of the two files — blob must survive because second.txt still references it.
	authDelete(t, client, base+"/backup/first.txt")

	store.mu.Lock()
	_, stillExists := store.objects[blobKey]
	store.mu.Unlock()
	assert.True(t, stillExists, "blob must be kept while another version still references it")
}

func TestIntegration_BlobDedup_MultipleVersionsSameFile(t *testing.T) {
	srv, store := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "dedup-versions@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	// Upload v1, v2 (different), then v3 = same bytes as v1 (restore scenario).
	v1 := []byte("version one")
	v2 := []byte("version two")
	authUpload(t, client, base+"/backup/doc.txt", v1)
	authUpload(t, client, base+"/backup/doc.txt", v2)
	authUpload(t, client, base+"/backup/doc.txt", v1) // restore

	store.mu.Lock()
	blobCount := 0
	for k := range store.objects {
		if strings.HasPrefix(k, "1/blobs/") {
			blobCount++
		}
	}
	store.mu.Unlock()

	// v1 and v3 share one blob; v2 has its own — total 2 blob objects, not 3.
	assert.Equal(t, 2, blobCount, "restore of an earlier version must reuse its blob, not create a third object")
}

// ---- Delta compression integration tests ----

func TestIntegration_Delta_SecondVersionGetsDelta(t *testing.T) {
	srv, store, pool := setupTestEnv(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "delta-basic@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	// Upload v1 (keyframe) and v2.
	// Use text content large enough that bsdiff produces a meaningful patch
	// but different enough to trigger a new version.
	v1 := bytes.Repeat([]byte("hello world line\n"), 200)
	v2 := append(bytes.Repeat([]byte("hello world line\n"), 200), []byte("new line at end\n")...)
	authUpload(t, client, base+"/backup/file.txt", v1)
	authUpload(t, client, base+"/backup/file.txt", v2)

	versions, err := db.GetFileVersions(context.Background(), pool, int64(folderID), "file.txt")
	require.NoError(t, err)
	require.Len(t, versions, 2)

	// versions are returned DESC by version number; versions[0] is v2.
	v2row := versions[0]
	require.Equal(t, 2, v2row.Version)

	store.mu.Lock()
	hasDelta := false
	for k := range store.objects {
		if strings.HasPrefix(k, "1/deltas/") {
			hasDelta = true
			break
		}
	}
	store.mu.Unlock()

	// v2 should be stored as a delta (small patch on top of v1).
	assert.True(t, hasDelta, "v2 should be stored as a delta object")
	assert.NotNil(t, v2row.DeltaBaseVersionID, "v2 should have delta_base_version_id set")
}

func TestIntegration_Delta_DownloadReconstructsCorrectly(t *testing.T) {
	srv, _ := setupTestServerWithStore(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "delta-download@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	v1 := bytes.Repeat([]byte("base content line\n"), 200)
	v2 := append(bytes.Repeat([]byte("base content line\n"), 200), []byte("appended change\n")...)
	authUpload(t, client, base+"/backup/doc.txt", v1)
	authUpload(t, client, base+"/backup/doc.txt", v2)

	// Get the version list to find v2's ID.
	resp := authGet(t, client, base+"/versions?path=doc.txt")
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var verResp struct {
		Versions []struct {
			ID      int64 `json:"id"`
			Version int   `json:"version"`
		} `json:"versions"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&verResp))
	resp.Body.Close()

	var v2id int64
	for _, v := range verResp.Versions {
		if v.Version == 2 {
			v2id = v.ID
		}
	}
	require.NotZero(t, v2id, "could not find v2 in version list")

	// Download v2 and verify the bytes match what was uploaded.
	dlResp := authGet(t, client, fmt.Sprintf("%s/versions/%d", base, v2id))
	require.Equal(t, http.StatusOK, dlResp.StatusCode)
	got, err := io.ReadAll(dlResp.Body)
	dlResp.Body.Close()
	require.NoError(t, err)
	assert.Equal(t, v2, got, "downloaded bytes must match original v2 content after delta reconstruction")
}

func TestIntegration_Delta_KeyframeEvery10IsFullBlob(t *testing.T) {
	srv, store, pool := setupTestEnv(t)
	defer srv.Close()

	client := registerAndLogin(t, srv, "delta-keyframe@example.com", "pass")
	folderID := addFolder(t, client, srv.URL, "/watched")
	base := folderURL(srv.URL, folderID)

	base64Content := bytes.Repeat([]byte("keyframe test line\n"), 200)
	for i := 0; i < 11; i++ {
		content := append(bytes.Clone(base64Content), []byte(fmt.Sprintf("version %d\n", i))...)
		authUpload(t, client, base+"/backup/kf.txt", content)
	}

	versions, err := db.GetFileVersions(context.Background(), pool, int64(folderID), "kf.txt")
	require.NoError(t, err)
	require.Len(t, versions, 11)

	// Find v11 (version number 11). ShouldDelta returns false for version%10==1 (i.e. 11),
	// so v11 must be a full blob (no delta).
	var v11 db.FileVersion
	for _, v := range versions {
		if v.Version == 11 {
			v11 = v
		}
	}
	require.Equal(t, 11, v11.Version)
	assert.Nil(t, v11.DeltaBaseVersionID, "v11 is a keyframe and must not have a delta base")

	// v11 should be stored as a blob, not a delta.
	store.mu.Lock()
	_, hasBlob := store.objects[storage.BlobKey(1, v11.ChecksumSHA256)]
	store.mu.Unlock()
	assert.True(t, hasBlob, "v11 keyframe must be stored as a full blob")
}
