// Package ratelimit defines the limiter interface and an in-memory implementation.
// The Redis implementation lives in internal/store/redis.
package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Limiter is a fixed-window counter. Fixed windows allow up to 2x the limit across a window
// boundary; that is acceptable for brute-force and abuse protection (docs/security.md).
type Limiter interface {
	// Allow records one hit for key and reports whether it is within limit for the window.
	// When not allowed, retryAfter is the time until the window resets.
	Allow(ctx context.Context, key string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration, err error)
}

type bucket struct {
	count   int
	resetAt time.Time
}

// Memory is a process-local Limiter for tests and single-instance development.
type Memory struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time
}

// NewMemory returns a Memory limiter. now may be nil.
func NewMemory(now func() time.Time) *Memory {
	if now == nil {
		now = time.Now
	}
	return &Memory{buckets: make(map[string]*bucket), now: now}
}

func (m *Memory) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := m.now()
	b, ok := m.buckets[key]
	if !ok || !n.Before(b.resetAt) {
		b = &bucket{resetAt: n.Add(window)}
		m.buckets[key] = b
	}
	b.count++
	if b.count > limit {
		return false, b.resetAt.Sub(n), nil
	}
	return true, 0, nil
}
