package postgres

import (
	"context"
	"fmt"
	"strings"

	"realtime-chat/backend/internal/domain"
)

const userCols = `id::text, email, display_name, status, created_at`

// CreateUser inserts a user. A duplicate email (case-insensitive) returns a conflict error.
func (s *Store) CreateUser(ctx context.Context, email, displayName, passwordHash string) (domain.User, error) {
	var u domain.User
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (email, display_name, password_hash) VALUES ($1, $2, $3) RETURNING `+userCols,
		email, displayName, passwordHash,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Status, &u.CreatedAt)
	if err != nil {
		if pgCode(err) == "23505" {
			return domain.User{}, domain.Conflict("Email is already registered")
		}
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}

// UserByEmail returns the user and password hash for a case-insensitive email match.
func (s *Store) UserByEmail(ctx context.Context, email string) (domain.User, string, error) {
	var (
		u    domain.User
		hash string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT `+userCols+`, password_hash FROM users WHERE lower(email) = lower($1)`, email,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Status, &u.CreatedAt, &hash)
	if err != nil {
		return domain.User{}, "", notFoundIfNoRows(err, "user by email")
	}
	return u, hash, nil
}

// UserByID returns a user by id.
func (s *Store) UserByID(ctx context.Context, id string) (domain.User, error) {
	var u domain.User
	err := s.pool.QueryRow(ctx, `SELECT `+userCols+` FROM users WHERE id = $1::uuid`, id,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Status, &u.CreatedAt)
	if err != nil {
		return domain.User{}, notFoundIfNoRows(err, "user by id")
	}
	return u, nil
}

// UpdateDisplayName changes a user's display name.
func (s *Store) UpdateDisplayName(ctx context.Context, id, name string) (domain.User, error) {
	var u domain.User
	err := s.pool.QueryRow(ctx,
		`UPDATE users SET display_name = $2 WHERE id = $1::uuid RETURNING `+userCols, id, name,
	).Scan(&u.ID, &u.Email, &u.DisplayName, &u.Status, &u.CreatedAt)
	if err != nil {
		return domain.User{}, notFoundIfNoRows(err, "update display name")
	}
	return u, nil
}

// UpdatePasswordHash replaces a stored password hash (used to upgrade Argon2 parameters).
func (s *Store) UpdatePasswordHash(ctx context.Context, id, hash string) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1::uuid`, id, hash)
	if err != nil {
		return fmt.Errorf("update password hash: %w", err)
	}
	return nil
}

// ActiveUserIDs returns which of ids belong to existing, active users.
func (s *Store) ActiveUserIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text FROM users WHERE status = 'active' AND id = ANY($1::text[]::uuid[])`, ids)
	if err != nil {
		return nil, fmt.Errorf("active users: %w", err)
	}
	defer rows.Close()
	found := make(map[string]bool, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		found[strings.ToLower(id)] = true
	}
	return found, rows.Err()
}
