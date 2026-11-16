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

	prefix := fmt.Sprintf("test:%d:", time.Now().UnixNano())
	l := rstore.NewLimiter(client, prefix)
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		ok, _, err := l.Allow(ctx, "k", 3, 2*time.Second)
		if err != nil || !ok {
			t.Fatalf("hit %d: ok=%v err=%v", i, ok, err)
		}
	}
	ttl, err := client.PTTL(ctx, prefix+"k").Result()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("TTL after third hit: %v", ttl)
	ok, retry, err := l.Allow(ctx, "k", 3, 2*time.Second)
	if err != nil || ok || retry <= 0 || retry > 2*time.Second {
		t.Fatalf("4th hit: ok=%v retry=%v err=%v", ok, retry, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		ttl, err := client.PTTL(ctx, prefix+"k").Result()
		if err != nil {
			t.Fatal(err)
		}

		if ttl == -2*time.Nanosecond {
			break // Key has expired.
		}

		if ttl == -1*time.Nanosecond {
			t.Fatal("rate-limit key exists without an expiry")
		}

		if time.Now().After(deadline) {
			t.Fatalf("rate-limit key did not expire; remaining TTL: %v", ttl)
		}

		time.Sleep(25 * time.Millisecond)
	}

	ok, _, err = l.Allow(ctx, "k", 3, 2*time.Second)
	if err != nil || !ok {
		t.Fatalf("after window: ok=%v err=%v", ok, err)
	}
}
