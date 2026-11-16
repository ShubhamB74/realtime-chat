package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"realtime-chat/backend/internal/domain"
)

// Store implements the repository interfaces of the application services on PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore wraps a connection pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// inTx runs fn in a transaction; it commits if fn returns nil and rolls back otherwise.
func (s *Store) inTx(ctx context.Context, fn func(tx pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after a successful commit
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// pgCode returns the SQLSTATE of err, or "".
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// notFoundIfNoRows maps pgx.ErrNoRows to domain.NotFound and wraps other errors.
func notFoundIfNoRows(err error, op string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NotFound()
	}
	return fmt.Errorf("%s: %w", op, err)
}
