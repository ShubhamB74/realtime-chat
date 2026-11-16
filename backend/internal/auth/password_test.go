package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func fast() Params { return Params{Memory: 8, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16} }

func TestHashVerifyRoundTrip(t *testing.T) {
	h := NewHasher(fast(), 2)
	enc, err := h.Hash(context.Background(), "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=8,t=1,p=1$") {
		t.Fatalf("unexpected format: %s", enc)
	}
	ok, rehash, err := h.Verify(context.Background(), "correct horse battery", enc)
	if err != nil || !ok || rehash {
		t.Fatalf("ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	ok, _, err = h.Verify(context.Background(), "wrong password", enc)
	if err != nil || ok {
		t.Fatalf("wrong password accepted: ok=%v err=%v", ok, err)
	}
}

func TestHashesAreSalted(t *testing.T) {
	h := NewHasher(fast(), 2)
	a, _ := h.Hash(context.Background(), "same-password")
	b, _ := h.Hash(context.Background(), "same-password")
	if a == b {
		t.Fatal("identical hashes: salt not random")
	}
}

func TestNeedsRehashWhenParamsChange(t *testing.T) {
	old := NewHasher(fast(), 2)
	enc, _ := old.Hash(context.Background(), "pw-for-rehash")
	stronger := fast()
	stronger.Time = 2
	ok, rehash, err := NewHasher(stronger, 2).Verify(context.Background(), "pw-for-rehash", enc)
	if err != nil || !ok || !rehash {
		t.Fatalf("ok=%v rehash=%v err=%v", ok, rehash, err)
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	h := NewHasher(fast(), 1)
	for _, enc := range []string{
		"", "plaintext", "$argon2i$v=19$m=8,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=8,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=8,t=1,p=1$!!!$aGFzaA",
	} {
		if ok, _, err := h.Verify(context.Background(), "x", enc); ok || !errors.Is(err, ErrMalformedHash) {
			t.Errorf("%q: ok=%v err=%v", enc, ok, err)
		}
	}
}

func TestHashRespectsContextWhenSaturated(t *testing.T) {
	h := NewHasher(fast(), 1)
	h.sem <- struct{}{} // occupy the only slot
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Hash(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}
