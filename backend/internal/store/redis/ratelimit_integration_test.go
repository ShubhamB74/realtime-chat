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
