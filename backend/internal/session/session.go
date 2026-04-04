package session

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// AccessTokenTTL is intentionally short — clients use refresh tokens to rotate.
	AccessTokenTTL = 1 * time.Minute
	// RefreshTokenTTL is the lifetime of an opaque refresh token.
	RefreshTokenTTL = 30 * 24 * time.Hour
	// PasswordResetTokenTTL is the lifetime of a password-reset token.
	PasswordResetTokenTTL = 1 * time.Hour
)

// Claims holds the JWT payload for access tokens.
type Claims struct {
	UserID   int64  `json:"user_id"`
	Username string `json:"username"`
	Email    string `json:"email"`
	jwt.RegisteredClaims
}

// Service creates and validates tokens.
type Service struct {
	secret []byte
}

// NewService creates a Service with the given signing secret.
func NewService(secret string) *Service {
	return &Service{secret: []byte(secret)}
}

// CreateAccessToken issues a short-lived signed JWT for the given user.
func (s *Service) CreateAccessToken(userID int64, username, email string) (string, error) {
	claims := Claims{
		UserID:   userID,
		Username: username,
		Email:    email,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(AccessTokenTTL)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "cloudbackupserver",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.secret)
}

// ValidateAccessToken parses and validates an access token JWT.
func (s *Service) ValidateAccessToken(tokenString string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.secret, nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, errors.New("invalid token")
	}
	return claims, nil
}

// GenerateRefreshToken creates a cryptographically random opaque token.
// Returns the raw token (sent to the client) and its SHA-256 hex hash (stored in DB).
func GenerateRefreshToken() (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generating random bytes: %w", err)
	}
	raw = base64.URLEncoding.EncodeToString(b)
	hash = HashToken(raw)
	return raw, hash, nil
}

// HashToken returns the SHA-256 hex digest of a token string.
// Use this to look up tokens in the database without storing the raw value.
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
