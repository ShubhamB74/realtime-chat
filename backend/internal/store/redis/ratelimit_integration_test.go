//go:build integration

// Needs a running Redis (`make up`) and REDIS_URL set.
package redis_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	rstore "realtime-chat/backend/internal/store/redis"
)

func TestRedisLimiter(t *testing.T) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Fatal("REDIS_URL must be set for integration tests")
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatal("invalid REDIS_URL")
	}
	client := goredis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	l := rstore.NewLimiter(client, fmt.Sprintf("test:%d:", time.Now().UnixNano()))
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		ok, _, err := l.Allow(ctx, "k", 3, 2*time.Second)
		if err != nil || !ok {
			t.Fatalf("hit %d: ok=%v err=%v", i, ok, err)
		}
	}
	ok, retry, err := l.Allow(ctx, "k", 3, 2*time.Second)
	if err != nil || ok || retry <= 0 || retry > 2*time.Second {
		t.Fatalf("4th hit: ok=%v retry=%v err=%v", ok, retry, err)
	}
	time.Sleep(2100 * time.Millisecond)
	if ok, _, err := l.Allow(ctx, "k", 3, 2*time.Second); err != nil || !ok {
		t.Fatalf("after window: ok=%v err=%v", ok, err)
	}
}

func TestPresenceTouchOnlineRemoveAndExpiry(t *testing.T) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Fatal("REDIS_URL must be set for integration tests")
	}
	opts, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatal("invalid REDIS_URL")
	}
	client := goredis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	p := rstore.NewPresence(client, fmt.Sprintf("test-presence:%d:", time.Now().UnixNano()))
	ctx := context.Background()

	if on, err := p.Online(ctx, []string{"u1", "u2"}); err != nil || on["u1"] || on["u2"] {
		t.Fatalf("nobody should be online: %v %v", on, err)
	}
	if err := p.Touch(ctx, "u1", "c1", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := p.Touch(ctx, "u1", "c2", 5*time.Second); err != nil { // second device
		t.Fatal(err)
	}
	on, err := p.Online(ctx, []string{"u1", "u2"})
	if err != nil || !on["u1"] || on["u2"] {
		t.Fatalf("online = %v err=%v", on, err)
	}
	_ = p.Remove(ctx, "u1", "c1")
	if on, _ := p.Online(ctx, []string{"u1"}); !on["u1"] {
		t.Error("u1 still has a second connection")
	}
	_ = p.Remove(ctx, "u1", "c2")
	if on, _ := p.Online(ctx, []string{"u1"}); on["u1"] {
		t.Error("u1 should be offline after both connections left")
	}
	// Entries expire on their own if a gateway dies without removing them.
	if err := p.Touch(ctx, "u3", "c9", 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if on, _ := p.Online(ctx, []string{"u3"}); on["u3"] {
		t.Error("expired connection must not count as online")
	}
}
