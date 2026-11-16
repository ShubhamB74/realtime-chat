package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// Limiter is a Redis-backed fixed-window rate limiter shared by all API replicas.
type Limiter struct {
	client *goredis.Client
	prefix string
}

// NewLimiter returns a Limiter. Keys are stored as "<prefix><key>" and expire with the window.
func NewLimiter(client *goredis.Client, prefix string) *Limiter {
	return &Limiter{client: client, prefix: prefix}
}

// The script increments and, on the first hit of a window, sets the expiry atomically, so a
// crash between INCR and PEXPIRE can never leave a counter that never expires.
var allowScript = goredis.NewScript(`
local c = redis.call('INCR', KEYS[1])
if c == 1 then redis.call('PEXPIRE', KEYS[1], ARGV[1]) end
local ttl = redis.call('PTTL', KEYS[1])
return {c, ttl}
`)

// Allow implements ratelimit.Limiter.
func (l *Limiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	res, err := allowScript.Run(ctx, l.client, []string{l.prefix + key}, window.Milliseconds()).Slice()
	if err != nil {
		return false, 0, fmt.Errorf("rate limit script: %w", err)
	}
	if len(res) != 2 {
		return false, 0, fmt.Errorf("rate limit script: unexpected result length %d", len(res))
	}
	count, ok1 := res[0].(int64)
	ttl, ok2 := res[1].(int64)
	if !ok1 || !ok2 {
		return false, 0, fmt.Errorf("rate limit script: unexpected result types")
	}
	if count > int64(limit) {
		if ttl < 0 {
			ttl = window.Milliseconds()
		}
		return false, time.Duration(ttl) * time.Millisecond, nil
	}
	return true, 0, nil
}
