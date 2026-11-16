// Package auth contains password hashing and token handling. It has no HTTP or database code.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the Argon2id cost parameters. Memory is in KiB.
type Params struct {
	Memory  uint32
	Time    uint32
	Threads uint8
	KeyLen  uint32
	SaltLen uint32
}

// DefaultParams: 64 MiB, 3 passes, 2 lanes. This is above the commonly cited OWASP minimum
// (19 MiB, t=2, p=1) but must be tuned by measuring on the real hardware (docs/security.md).
func DefaultParams() Params {
	return Params{Memory: 64 * 1024, Time: 3, Threads: 2, KeyLen: 32, SaltLen: 16}
}

// Hasher hashes and verifies passwords with Argon2id. A semaphore bounds concurrent hashing
// so a burst of logins cannot exhaust memory (each hash needs Params.Memory).
type Hasher struct {
	p   Params
	sem chan struct{}
}

// NewHasher returns a Hasher. maxConcurrent <= 0 defaults to the CPU count.
func NewHasher(p Params, maxConcurrent int) *Hasher {
	if maxConcurrent <= 0 {
		maxConcurrent = runtime.NumCPU()
	}
	return &Hasher{p: p, sem: make(chan struct{}, maxConcurrent)}
}

func (h *Hasher) acquire(ctx context.Context) (release func(), err error) {
	select {
	case h.sem <- struct{}{}:
		return func() { <-h.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Hash returns a PHC-format string: $argon2id$v=19$m=..,t=..,p=..$salt$hash
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	release, err := h.acquire(ctx)
	if err != nil {
		return "", err
	}
	defer release()

	salt := make([]byte, h.p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, h.p.Time, h.p.Memory, h.p.Threads, h.p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.p.Memory, h.p.Time, h.p.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// ErrMalformedHash is returned when a stored hash cannot be parsed.
var ErrMalformedHash = errors.New("malformed password hash")

// Verify checks password against encoded. needsRehash is true when the stored parameters
// differ from the current ones (call Hash and store the result after a successful login).
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (ok, needsRehash bool, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, false, ErrMalformedHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, false, ErrMalformedHash
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return false, false, ErrMalformedHash
	}
	if p.Memory == 0 || p.Time == 0 || p.Threads == 0 {
		return false, false, ErrMalformedHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, false, ErrMalformedHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, false, ErrMalformedHash
	}
	p.KeyLen = uint32(len(want)) //nolint:gosec // length of a decoded hash, bounded by the stored string
	p.SaltLen = uint32(len(salt)) //nolint:gosec // as above

	release, err := h.acquire(ctx)
	if err != nil {
		return false, false, err
	}
	defer release()

	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	if subtle.ConstantTimeCompare(want, got) != 1 {
		return false, false, nil
	}
	return true, p != h.p, nil
}
