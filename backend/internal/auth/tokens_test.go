package auth

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var secret = []byte("0123456789abcdef0123456789abcdef-test")

func issuerAt(t *testing.T, now *time.Time, ttl time.Duration) *TokenIssuer {
	t.Helper()
	ti, err := NewTokenIssuer(secret, "test-issuer", ttl, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	return ti
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	now := time.Now()
	ti := issuerAt(t, &now, 15*time.Minute)
	tok, err := ti.Issue("user-1", "sess-1", "dev-1")
	if err != nil {
		t.Fatal(err)
	}
	id, err := ti.Verify(tok)
	if err != nil || id != (Identity{UserID: "user-1", SessionID: "sess-1", DeviceID: "dev-1"}) {
		t.Fatalf("id=%+v err=%v", id, err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	now := time.Now()
	ti := issuerAt(t, &now, time.Minute)
	tok, _ := ti.Issue("u", "s", "d")
	now = now.Add(2 * time.Minute)
	if _, err := ti.Verify(tok); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestVerifyRejectsWrongSecretIssuerAndTampering(t *testing.T) {
	now := time.Now()
	ti := issuerAt(t, &now, time.Minute)
	tok, _ := ti.Issue("u", "s", "d")

	other, _ := NewTokenIssuer([]byte(strings.Repeat("x", 40)), "test-issuer", time.Minute, func() time.Time { return now })
	if _, err := other.Verify(tok); err == nil {
		t.Error("token signed with a different secret accepted")
	}
	diffIssuer, _ := NewTokenIssuer(secret, "someone-else", time.Minute, func() time.Time { return now })
	if _, err := diffIssuer.Verify(tok); err == nil {
		t.Error("token from another issuer accepted")
	}
	parts := strings.Split(tok, ".")
	parts[1] = parts[1][:len(parts[1])-2] + "AA"
	if _, err := ti.Verify(strings.Join(parts, ".")); err == nil {
		t.Error("tampered payload accepted")
	}
	if _, err := ti.Verify("not-a-jwt"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestVerifyRejectsAlgNone(t *testing.T) {
	now := time.Now()
	ti := issuerAt(t, &now, time.Minute)
	c := claims{SessionID: "s", DeviceID: "d", RegisteredClaims: jwt.RegisteredClaims{
		Issuer: "test-issuer", Subject: "u", ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, c).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ti.Verify(unsigned); err == nil {
		t.Fatal("alg=none token accepted")
	}
}

func TestNewTokenIssuerValidation(t *testing.T) {
	if _, err := NewTokenIssuer([]byte("short"), "i", time.Minute, nil); err == nil {
		t.Error("short secret accepted")
	}
	if _, err := NewTokenIssuer(secret, "i", 0, nil); err == nil {
		t.Error("zero ttl accepted")
	}
}

func TestRefreshTokens(t *testing.T) {
	a, ha, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := NewRefreshToken()
	if a == b || len(a) != 43 || len(ha) != 32 {
		t.Fatalf("len(a)=%d len(hash)=%d equal=%v", len(a), len(ha), a == b)
	}
	if !bytes.Equal(ha, HashRefreshToken(a)) {
		t.Error("hash is not reproducible from the token")
	}
}
