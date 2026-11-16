package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestMemoryLimiterWindow(t *testing.T) {
	now := time.Unix(1000, 0)
	m := NewMemory(func() time.Time { return now })
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		ok, _, _ := m.Allow(ctx, "k", 3, time.Minute)
		if !ok {
			t.Fatalf("hit %d should be allowed", i+1)
		}
	}
	ok, retry, _ := m.Allow(ctx, "k", 3, time.Minute)
	if ok || retry <= 0 || retry > time.Minute {
		t.Fatalf("4th hit: ok=%v retry=%v", ok, retry)
	}
	if ok, _, _ := m.Allow(ctx, "other", 3, time.Minute); !ok {
		t.Error("keys must be independent")
	}
	now = now.Add(61 * time.Second)
	if ok, _, _ := m.Allow(ctx, "k", 3, time.Minute); !ok {
		t.Error("window should have reset")
	}
}
