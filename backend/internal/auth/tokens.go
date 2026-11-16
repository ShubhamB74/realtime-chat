package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Identity is what a verified access token proves.
type Identity struct {
	UserID    string
	SessionID string // session family id
	DeviceID  string
}

type claims struct {
	SessionID string `json:"sid"`
	DeviceID  string `json:"did"`
	jwt.RegisteredClaims
}

// ErrInvalidToken is returned for any token that fails verification. Callers must not
// distinguish causes to clients.
var ErrInvalidToken = errors.New("invalid token")

// TokenIssuer signs and verifies short-lived HS256 access tokens (ADR-009).
type TokenIssuer struct {
	key    []byte
	issuer string
	ttl    time.Duration
	now    func() time.Time
}

// MinSecretBytes is the minimum accepted signing-secret length.
const MinSecretBytes = 32

// NewTokenIssuer validates the secret length. now may be nil (uses time.Now).
func NewTokenIssuer(secret []byte, issuer string, ttl time.Duration, now func() time.Time) (*TokenIssuer, error) {
	if len(secret) < MinSecretBytes {
		return nil, fmt.Errorf("signing secret must be at least %d bytes", MinSecretBytes)
	}
	if ttl <= 0 {
		return nil, errors.New("access token ttl must be positive")
	}
	if now == nil {
		now = time.Now
	}
	return &TokenIssuer{key: secret, issuer: issuer, ttl: ttl, now: now}, nil
}

// TTL returns the access token lifetime.
func (t *TokenIssuer) TTL() time.Duration { return t.ttl }

// Issue creates a signed access token.
func (t *TokenIssuer) Issue(userID, sessionID, deviceID string) (string, error) {
	n := t.now()
	c := claims{
		SessionID: sessionID,
		DeviceID:  deviceID,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    t.issuer,
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(n),
			ExpiresAt: jwt.NewNumericDate(n.Add(t.ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(t.key)
}

// Verify checks signature, algorithm, issuer and expiry.
func (t *TokenIssuer) Verify(raw string) (Identity, error) {
	var c claims
	_, err := jwt.ParseWithClaims(raw, &c,
		func(*jwt.Token) (any, error) { return t.key, nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(t.issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(t.now),
		jwt.WithLeeway(5*time.Second),
	)
	if err != nil || c.Subject == "" || c.SessionID == "" || c.DeviceID == "" {
		return Identity{}, ErrInvalidToken
	}
	return Identity{UserID: c.Subject, SessionID: c.SessionID, DeviceID: c.DeviceID}, nil
}

// NewRefreshToken returns a random opaque token (256 bits, base64url) and its SHA-256 digest.
// Only the digest is stored.
func NewRefreshToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("generate refresh token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashRefreshToken(token), nil
}

// HashRefreshToken returns the SHA-256 digest of a refresh token string.
func HashRefreshToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
