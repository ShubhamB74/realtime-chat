package ws

import "time"

// bucket is a token bucket used to rate-limit inbound events of one connection. It is only
// touched by the connection's read goroutine, so it needs no locking.
type bucket struct {
	tokens float64
	last   time.Time
	rate   float64 // tokens per second
	burst  float64
	now    func() time.Time
}

func newBucket(rate float64, burst int, now func() time.Time) *bucket {
	if now == nil {
		now = time.Now
	}
	return &bucket{tokens: float64(burst), last: now(), rate: rate, burst: float64(burst), now: now}
}

func (b *bucket) allow() bool {
	n := b.now()
	b.tokens += n.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = n
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
