// Package redis opens the Redis client. Redis is a non-authoritative index for presence,
// connection routing and pub/sub (ADR-006); it must never be the source of truth.
package redis

import (
	"context"
	"errors"
	"log/slog"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"realtime-chat/backend/internal/platform/retry"
)

// Connect creates a client and verifies connectivity with retry, backoff and jitter.
func Connect(ctx context.Context, redisURL string, logger *slog.Logger) (*goredis.Client, error) {
	opts, err := goredis.ParseURL(redisURL)
	if err != nil {
		// The parse error can echo the URL, so do not propagate it.
		return nil, errors.New("invalid redis configuration")
	}
	opts.DialTimeout = 3 * time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = 2 * time.Second

	client := goredis.NewClient(opts)
	err = retry.Do(ctx, retry.DefaultPolicy(), func(ctx context.Context) error {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		return client.Ping(pingCtx).Err()
	}, func(attempt int, err error, wait time.Duration) {
		logger.Warn("redis not ready, retrying",
			slog.Int("attempt", attempt),
			slog.Duration("wait", wait),
			slog.String("error", err.Error()),
		)
	})
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}
