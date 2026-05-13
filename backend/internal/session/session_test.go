package session_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ali-sab/cloudbackupserver/backend/internal/session"
)

func TestHashToken_Deterministic(t *testing.T) {
	h1 := session.HashToken("some-token")
	h2 := session.HashToken("some-token")
	if h1 != h2 {
		t.Fatalf("HashToken not deterministic: %q != %q", h1, h2)
	}
}

func TestHashToken_DifferentInputs(t *testing.T) {
	if session.HashToken("a") == session.HashToken("b") {
		t.Fatal("HashToken produced same hash for different inputs")
	}
}

func TestHashToken_IsHex(t *testing.T) {
	h := session.HashToken("test")
	if len(h) != 64 {
		t.Fatalf("expected 64-char SHA-256 hex, got len %d: %q", len(h), h)
	}
	for _, c := range h {
		if !strings.ContainsRune("0123456789abcdef", c) {
			t.Fatalf("non-hex character %q in hash %q", c, h)
		}
	}
}

func TestGenerateRefreshToken_Unique(t *testing.T) {
	raw1, hash1, err := session.GenerateRefreshToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw2, hash2, err := session.GenerateRefreshToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if raw1 == raw2 {
		t.Fatal("two generated refresh tokens are identical")
	}
	if hash1 == hash2 {
		t.Fatal("two generated refresh token hashes are identical")
	}
}

func TestGenerateRefreshToken_HashMatchesRaw(t *testing.T) {
	raw, hash, err := session.GenerateRefreshToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if session.HashToken(raw) != hash {
		t.Fatal("returned hash does not match HashToken(raw)")
	}
}

func TestCreateAndValidateAccessToken(t *testing.T) {
	svc := session.NewService("test-secret-key")
	tok, err := svc.CreateAccessToken(42, "user@example.com")
	if err != nil {
		t.Fatalf("CreateAccessToken: %v", err)
	}

	claims, err := svc.ValidateAccessToken(tok)
	if err != nil {
		t.Fatalf("ValidateAccessToken: %v", err)
	}
	if claims.UserID != 42 {
		t.Errorf("expected UserID 42, got %d", claims.UserID)
	}
	if claims.Email != "user@example.com" {
		t.Errorf("expected email user@example.com, got %q", claims.Email)
	}
}

func TestValidateAccessToken_WrongSecret(t *testing.T) {
	svc1 := session.NewService("secret-a")
	svc2 := session.NewService("secret-b")

	tok, _ := svc1.CreateAccessToken(1, "a@example.com")
	if _, err := svc2.ValidateAccessToken(tok); err == nil {
		t.Fatal("expected error validating token with wrong secret, got nil")
	}
}

func TestValidateAccessToken_MalformedToken(t *testing.T) {
	svc := session.NewService("secret")
	if _, err := svc.ValidateAccessToken("not.a.jwt"); err == nil {
		t.Fatal("expected error for malformed token, got nil")
	}
}

func TestAccessTokenTTL(t *testing.T) {
	if session.AccessTokenTTL != 5*time.Minute {
		t.Errorf("expected AccessTokenTTL 5m, got %v", session.AccessTokenTTL)
	}
}

func TestRefreshTokenTTL(t *testing.T) {
	if session.RefreshTokenTTL != 30*24*time.Hour {
		t.Errorf("expected RefreshTokenTTL 720h, got %v", session.RefreshTokenTTL)
	}
}
