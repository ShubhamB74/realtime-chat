//go:build integration

// These tests need a PostgreSQL 18 database that has been migrated (`make migrate-up`).
// Run with: make test-integration  (DATABASE_URL must be set).
package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Fatal("DATABASE_URL must be set for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)
	if err := p.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return p
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func newUser(t *testing.T, p *pgxpool.Pool, email string) string {
	t.Helper()
	var id string
	err := p.QueryRow(context.Background(),
		`INSERT INTO users (email, display_name, password_hash) VALUES ($1, 'Test', 'x') RETURNING id::text`,
		email).Scan(&id)
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() { _, _ = p.Exec(context.Background(), `DELETE FROM users WHERE id = $1::uuid`, id) })
	return id
}

func uniqueEmail() string { return fmt.Sprintf("it-%d@example.test", time.Now().UnixNano()) }

func TestTablesExist(t *testing.T) {
	p := pool(t)
	for _, name := range []string{"users", "devices", "user_sessions"} {
		var exists bool
		err := p.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			name).Scan(&exists)
		if err != nil || !exists {
			t.Errorf("table %s missing (err=%v)", name, err)
		}
	}
}

func TestEmailUniquenessIsCaseInsensitive(t *testing.T) {
	p := pool(t)
	email := uniqueEmail()
	newUser(t, p, email)
	_, err := p.Exec(context.Background(),
		`INSERT INTO users (email, display_name, password_hash) VALUES (upper($1), 'Dup', 'x')`, email)
	if sqlState(err) != "23505" {
		t.Fatalf("want unique violation 23505, got %v", err)
	}
}

func TestUserStatusConstraint(t *testing.T) {
	p := pool(t)
	_, err := p.Exec(context.Background(),
		`INSERT INTO users (email, display_name, password_hash, status) VALUES ($1, 'X', 'x', 'bogus')`, uniqueEmail())
	if sqlState(err) != "23514" {
		t.Fatalf("want check violation 23514, got %v", err)
	}
}

func TestDeviceRequiresExistingUser(t *testing.T) {
	p := pool(t)
	_, err := p.Exec(context.Background(),
		`INSERT INTO devices (user_id, name, platform) VALUES (gen_random_uuid(), 'x', 'web')`)
	if sqlState(err) != "23503" {
		t.Fatalf("want foreign key violation 23503, got %v", err)
	}
}

func TestSessionCannotUseAnotherUsersDevice(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	userA := newUser(t, p, uniqueEmail())
	userB := newUser(t, p, uniqueEmail())
	var deviceA string
	if err := p.QueryRow(ctx,
		`INSERT INTO devices (user_id, name, platform) VALUES ($1::uuid, 'laptop', 'web') RETURNING id::text`, userA).Scan(&deviceA); err != nil {
		t.Fatal(err)
	}
	hash := make([]byte, 32)
	_, err := p.Exec(ctx,
		`INSERT INTO user_sessions (user_id, device_id, family_id, refresh_token_hash, expires_at)
		 VALUES ($1::uuid, $2::uuid, gen_random_uuid(), $3, now() + interval '1 hour')`, userB, deviceA, hash)
	if sqlState(err) != "23503" {
		t.Fatalf("want foreign key violation 23503, got %v", err)
	}
}

func TestSessionTokenHashUniqueAndLength(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	user := newUser(t, p, uniqueEmail())
	var device string
	if err := p.QueryRow(ctx,
		`INSERT INTO devices (user_id, name, platform) VALUES ($1::uuid, 'phone', 'ios') RETURNING id::text`, user).Scan(&device); err != nil {
		t.Fatal(err)
	}
	insert := func(hash []byte) error {
		_, err := p.Exec(ctx,
			`INSERT INTO user_sessions (user_id, device_id, family_id, refresh_token_hash, expires_at)
			 VALUES ($1::uuid, $2::uuid, gen_random_uuid(), $3, now() + interval '1 hour')`, user, device, hash)
		return err
	}
	hash := make([]byte, 32)
	hash[0] = byte(time.Now().UnixNano())
	hash[1] = 7
	if err := insert(hash); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if sqlState(insert(hash)) != "23505" {
		t.Error("duplicate token hash should violate unique index")
	}
	if sqlState(insert([]byte("short"))) != "23514" {
		t.Error("short hash should violate length check")
	}
}

func TestDeletingUserCascades(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	user := newUser(t, p, uniqueEmail())
	var device string
	if err := p.QueryRow(ctx,
		`INSERT INTO devices (user_id, name, platform) VALUES ($1::uuid, 'tab', 'android') RETURNING id::text`, user).Scan(&device); err != nil {
		t.Fatal(err)
	}
	hash := make([]byte, 32)
	hash[0], hash[1], hash[2] = 9, 9, byte(time.Now().UnixNano())
	if _, err := p.Exec(ctx,
		`INSERT INTO user_sessions (user_id, device_id, family_id, refresh_token_hash, expires_at)
		 VALUES ($1::uuid, $2::uuid, gen_random_uuid(), $3, now() + interval '1 hour')`, user, device, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, user); err != nil {
		t.Fatal(err)
	}
	var devices, sessions int
	_ = p.QueryRow(ctx, `SELECT count(*) FROM devices WHERE user_id = $1::uuid`, user).Scan(&devices)
	_ = p.QueryRow(ctx, `SELECT count(*) FROM user_sessions WHERE user_id = $1::uuid`, user).Scan(&sessions)
	if devices != 0 || sessions != 0 {
		t.Errorf("cascade failed: devices=%d sessions=%d", devices, sessions)
	}
}

func TestUpdatedAtTrigger(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	id := newUser(t, p, uniqueEmail())
	var before time.Time
	if err := p.QueryRow(ctx, `SELECT updated_at FROM users WHERE id = $1::uuid`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	var after time.Time
	if err := p.QueryRow(ctx,
		`UPDATE users SET display_name = 'Changed' WHERE id = $1::uuid RETURNING updated_at`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.After(before) {
		t.Errorf("updated_at not advanced: before=%v after=%v", before, after)
	}
}
