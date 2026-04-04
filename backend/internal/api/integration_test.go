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
// The TEST_DATABASE_URL environment variable must be set; otherwise the test is skipped.
// All users are truncated after the test.
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
		_, _ = pool.Exec(context.Background(), "TRUNCATE users RESTART IDENTITY CASCADE")
		pool.Close()
	})

	svc := session.NewService("integration-test-jwt-secret")
	router := api.NewRouter(pool, svc)
	return httptest.NewServer(router)
}

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

	body := bytes.NewBufferString(`{"username":"alice","email":"alice@example.com","password":"secret123"}`)
	resp, err := http.Post(srv.URL+"/api/auth/register", "application/json", body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var auth api.AuthResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&auth))
	assert.NotEmpty(t, auth.Token)
	assert.Equal(t, "alice", auth.User.Username)
	assert.Equal(t, "alice@example.com", auth.User.Email)
}

func TestIntegration_RegisterDuplicateUsername(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	body := `{"username":"dupuser","email":"dup@example.com","password":"pass"}`
	http.Post(srv.URL+"/api/auth/register", "application/json", bytes.NewBufferString(body)) //nolint:errcheck

	resp, err := http.Post(srv.URL+"/api/auth/register", "application/json", bytes.NewBufferString(body))
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestIntegration_LoginSuccess(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	// Register first
	regBody := `{"username":"bob","email":"bob@example.com","password":"mypassword"}`
	_, err := http.Post(srv.URL+"/api/auth/register", "application/json", bytes.NewBufferString(regBody))
	require.NoError(t, err)

	// Login
	loginBody := `{"username":"bob","password":"mypassword"}`
	resp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewBufferString(loginBody))
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var auth api.AuthResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&auth))
	assert.NotEmpty(t, auth.Token)
	assert.Equal(t, "bob", auth.User.Username)
}

func TestIntegration_LoginWrongPassword(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	regBody := `{"username":"carol","email":"carol@example.com","password":"correct"}`
	http.Post(srv.URL+"/api/auth/register", "application/json", bytes.NewBufferString(regBody)) //nolint:errcheck

	loginBody := `{"username":"carol","password":"wrong"}`
	resp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewBufferString(loginBody))
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestIntegration_SessionFlow(t *testing.T) {
	srv := setupTestServer(t)
	defer srv.Close()

	// Unauthenticated session check
	resp, err := http.Get(srv.URL + "/api/session")
	require.NoError(t, err)
	var session api.SessionResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&session))
	assert.False(t, session.LoggedIn)

	// Register and capture token
	regBody := `{"username":"dave","email":"dave@example.com","password":"pass456"}`
	regResp, err := http.Post(srv.URL+"/api/auth/register", "application/json", bytes.NewBufferString(regBody))
	require.NoError(t, err)
	var auth api.AuthResponse
	require.NoError(t, json.NewDecoder(regResp.Body).Decode(&auth))

	// Authenticated session check
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/session", srv.URL), nil)
	req.Header.Set("Authorization", "Bearer "+auth.Token)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var authedSession api.SessionResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&authedSession))
	assert.True(t, authedSession.LoggedIn)
	require.NotNil(t, authedSession.User)
	assert.Equal(t, "dave", authedSession.User.Username)
}
