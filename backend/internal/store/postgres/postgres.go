// Package postgres opens the PostgreSQL connection pool. PostgreSQL is the source of truth for
// durable data (ADR-003).
package postgres

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"realtime-chat/backend/internal/platform/retry"
)

// Connect creates a pool and verifies connectivity, retrying with backoff and jitter so the
// service tolerates the database starting a little later than it does.
// maxConns <= 0 keeps the driver default.
func Connect(ctx context.Context, databaseURL string, maxConns int32, logger *slog.Logger) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// The parse error can echo the URL, so do not propagate it.
		return nil, errors.New("invalid database configuration")
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second

	var pool *pgxpool.Pool
	err = retry.Do(ctx, retry.DefaultPolicy(), func(ctx context.Context) error {
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			return err
		}
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := p.Ping(pingCtx); err != nil {
			p.Close()
			return err
		}
		pool = p
		return nil
	}, func(attempt int, err error, wait time.Duration) {
		logger.Warn("postgres not ready, retrying",
			slog.Int("attempt", attempt),
			slog.Duration("wait", wait),
			slog.String("error", err.Error()),
		)
	})
	if err != nil {
		return nil, err
	}
	return pool, nil
}
