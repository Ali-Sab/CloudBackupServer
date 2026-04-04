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
			"TRUNCATE password_reset_tokens, refresh_tokens, users RESTART IDENTITY CASCADE")
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
		`{"username":"alice","email":"alice@example.com","password":"secret123"}`)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var auth api.AuthResponse
	decodeJSON(t, resp, &auth)
	assert.NotEmpty(t, auth.AccessToken)
	assert.NotEmpty(t, auth.RefreshToken)
	assert.Equal(t, "alice", auth.User.Username)
}

func TestIntegration_RegisterDuplicateUsername(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	body := `{"username":"dupuser","email":"dup@example.com","password":"pass"}`
	postJSON(t, srv.URL+"/api/auth/register", body)
	resp := postJSON(t, srv.URL+"/api/auth/register", body)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestIntegration_LoginSuccess(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	postJSON(t, srv.URL+"/api/auth/register",
		`{"username":"bob","email":"bob@example.com","password":"mypassword"}`)

	resp := postJSON(t, srv.URL+"/api/auth/login",
		`{"username":"bob","password":"mypassword"}`)
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
		`{"username":"carol","email":"carol@example.com","password":"correct"}`)

	resp := postJSON(t, srv.URL+"/api/auth/login", `{"username":"carol","password":"wrong"}`)
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
		`{"username":"dave","email":"dave@example.com","password":"pass456"}`)
	var auth api.AuthResponse
	decodeJSON(t, regResp, &auth)

	// Authenticated session check
	resp = authGet(t, srv.URL+"/api/session", auth.AccessToken)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	var authed api.SessionResponse
	decodeJSON(t, resp, &authed)
	assert.True(t, authed.LoggedIn)
	require.NotNil(t, authed.User)
	assert.Equal(t, "dave", authed.User.Username)
}

func TestIntegration_RefreshTokenRotation(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	// Register and get initial tokens
	regResp := postJSON(t, srv.URL+"/api/auth/register",
		`{"username":"eve","email":"eve@example.com","password":"pass"}`)
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
	// Tokens must be different from the originals
	assert.NotEqual(t, auth.AccessToken, refreshed.AccessToken)
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
		`{"username":"frank","email":"frank@example.com","password":"pass"}`)
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
		`{"username":"grace","email":"grace@example.com","password":"pass"}`)
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
		`{"username":"henry","email":"henry@example.com","password":"oldpass"}`)

	// Forgot password
	fpResp := postJSON(t, srv.URL+"/api/auth/forgot-password", `{"username":"henry"}`)
	assert.Equal(t, http.StatusOK, fpResp.StatusCode)

	var fp api.ForgotPasswordResponse
	decodeJSON(t, fpResp, &fp)
	require.NotEmpty(t, fp.ResetToken, "dev mode must return reset_token")

	// Reset password
	resetResp := postJSON(t, srv.URL+"/api/auth/reset-password",
		fmt.Sprintf(`{"reset_token":%q,"new_password":"newpass"}`, fp.ResetToken))
	assert.Equal(t, http.StatusOK, resetResp.StatusCode)

	// Old password must no longer work
	resp := postJSON(t, srv.URL+"/api/auth/login", `{"username":"henry","password":"oldpass"}`)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// New password must work
	resp = postJSON(t, srv.URL+"/api/auth/login", `{"username":"henry","password":"newpass"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestIntegration_ResetTokenSingleUse(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	postJSON(t, srv.URL+"/api/auth/register",
		`{"username":"iris","email":"iris@example.com","password":"pass"}`)

	fpResp := postJSON(t, srv.URL+"/api/auth/forgot-password", `{"username":"iris"}`)
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

	// Unknown username — must return 200 with no reset_token (prevents enumeration)
	resp := postJSON(t, srv.URL+"/api/auth/forgot-password", `{"username":"nobody"}`)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var fp api.ForgotPasswordResponse
	decodeJSON(t, resp, &fp)
	assert.Empty(t, fp.ResetToken)
	assert.NotEmpty(t, fp.Message)
}
