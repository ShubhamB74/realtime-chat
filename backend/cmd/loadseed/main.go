// Command loadseed creates users, devices, sessions, group conversations and long-lived access
// tokens directly in the database, for load tests (tests/load). It bypasses registration and
// login on purpose: those endpoints are rate limited and deliberately expensive (Argon2id).
//
// DEVELOPMENT ONLY. The created users cannot log in. It refuses to run when APP_ENV=production.
// The output file contains access tokens: it is written with mode 0600 and git-ignored.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/store/postgres"
)

type seedUser struct {
	Index          int    `json:"index"`
	UserID         string `json:"user_id"`
	AccessToken    string `json:"access_token"`
	ConversationID string `json:"conversation_id"`
}

type seedFile struct {
	CreatedAt string     `json:"created_at"`
	Users     []seedUser `json:"users"`
	Groups    []string   `json:"groups"`
}

func main() { os.Exit(run()) }

func run() int {
	users := flag.Int("users", 200, "number of users to create")
	groupSize := flag.Int("group-size", 10, "users per group conversation (the last group absorbs the remainder)")
	ttl := flag.Duration("ttl", 4*time.Hour, "lifetime of the generated access tokens and sessions")
	out := flag.String("out", "tests/load/data/seed.json", "output file (contains tokens; mode 0600)")
	cleanup := flag.Bool("cleanup", false, "delete all previously seeded load-test users and their conversations, then exit")
	flag.Parse()

	if os.Getenv("APP_ENV") == "production" {
		fmt.Fprintln(os.Stderr, "loadseed refuses to run with APP_ENV=production")
		return 2
	}
	dbURL, secret := os.Getenv("DATABASE_URL"), os.Getenv("AUTH_JWT_SECRET")
	if dbURL == "" || (secret == "" && !*cleanup) {
		fmt.Fprintln(os.Stderr, "DATABASE_URL and AUTH_JWT_SECRET must be set (they come from .env: run `make env`)")
		return 2
	}
	issuer := os.Getenv("AUTH_ISSUER")
	if issuer == "" {
		issuer = "realtime-chat"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot connect to the database")
		return 1
	}
	defer pool.Close()

	if *cleanup {
		return cleanupSeed(ctx, pool)
	}
	if *users < 2 || *groupSize < 2 || *groupSize > 255 {
		fmt.Fprintln(os.Stderr, "need -users >= 2 and 2 <= -group-size <= 255")
		return 2
	}

	store := postgres.NewStore(pool)
	tokens, err := auth.NewTokenIssuer([]byte(secret), issuer, *ttl, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid AUTH_JWT_SECRET:", err)
		return 2
	}

	run := time.Now().UnixNano()
	seed := seedFile{CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	for i := 0; i < *users; i++ {
		u, err := store.CreateUser(ctx, fmt.Sprintf("load-%d-%d@example.test", run, i), fmt.Sprintf("Load User %d", i), "!load-test-user-cannot-log-in")
		if err != nil {
			fmt.Fprintf(os.Stderr, "create user %d: %v\n", i, err)
			return 1
		}
		hash := make([]byte, 32)
		if _, err := rand.Read(hash); err != nil {
			return 1
		}
		dev, sess, err := store.CreateLoginSession(ctx, domain.CreateLoginParams{
			UserID: u.ID, DeviceName: "k6", Platform: "other", TokenHash: hash, TTL: *ttl, MaxDevices: 100,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "create session %d: %v\n", i, err)
			return 1
		}
		tok, err := tokens.Issue(u.ID, sess.FamilyID, dev.ID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "issue token %d: %v\n", i, err)
			return 1
		}
		seed.Users = append(seed.Users, seedUser{Index: i, UserID: u.ID, AccessToken: tok})
	}

	groups := *users / *groupSize
	if groups < 1 {
		groups = 1
	}
	for g := 0; g < groups; g++ {
		var members []int
		for i := range seed.Users {
			if min(i / *groupSize, groups-1) == g {
				members = append(members, i)
			}
		}
		others := make([]string, 0, len(members)-1)
		for _, i := range members[1:] {
			others = append(others, seed.Users[i].UserID)
		}
		conv, err := store.CreateGroup(ctx, seed.Users[members[0]].UserID, fmt.Sprintf("load-group-%d", g), others)
		if err != nil {
			fmt.Fprintf(os.Stderr, "create group %d: %v\n", g, err)
			return 1
		}
		seed.Groups = append(seed.Groups, conv.ID)
		for _, i := range members {
			seed.Users[i].ConversationID = conv.ID
		}
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o750); err != nil {
		fmt.Fprintln(os.Stderr, "cannot create output directory:", err)
		return 1
	}
	b, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		return 1
	}
	if err := os.WriteFile(*out, b, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "cannot write output:", err)
		return 1
	}
	fmt.Printf("seeded %d users in %d groups -> %s (tokens valid for %s)\n", len(seed.Users), len(seed.Groups), *out, *ttl)
	return 0
}

func cleanupSeed(ctx context.Context, pool *pgxpool.Pool) int {
	tag, err := pool.Exec(ctx,
		`DELETE FROM conversations WHERE id IN (
		   SELECT cm.conversation_id FROM conversation_members cm JOIN users u ON u.id = cm.user_id
		   WHERE u.email LIKE 'load-%@example.test')`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cleanup conversations failed")
		return 1
	}
	convs := tag.RowsAffected()
	tag, err = pool.Exec(ctx, `DELETE FROM users WHERE email LIKE 'load-%@example.test'`)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cleanup users failed")
		return 1
	}
	fmt.Printf("deleted %d conversations and %d users\n", convs, tag.RowsAffected())
	return 0
}
