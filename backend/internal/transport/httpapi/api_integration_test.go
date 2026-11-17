//go:build integration

// End-to-end API tests against a real PostgreSQL (migrated) with the real services.
// Run with: make test-integration  (DATABASE_URL must be set).
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"realtime-chat/backend/internal/app/accounts"
	"realtime-chat/backend/internal/app/chats"
	"realtime-chat/backend/internal/app/messages"
	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/platform/httpx"
	"realtime-chat/backend/internal/platform/ratelimit"
	"realtime-chat/backend/internal/store/postgres"
	"realtime-chat/backend/internal/transport/httpapi"
	"realtime-chat/backend/internal/transport/ws"
)

// memPresence is an in-memory presence store shared by the hub and the REST API in tests.
type memPresence struct {
	mu    sync.Mutex
	conns map[string]map[string]bool
}

func (m *memPresence) Touch(_ context.Context, user, conn string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conns == nil {
		m.conns = map[string]map[string]bool{}
	}
	if m.conns[user] == nil {
		m.conns[user] = map[string]bool{}
	}
	m.conns[user][conn] = true
	return nil
}

func (m *memPresence) Remove(_ context.Context, user, conn string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conns[user], conn)
	return nil
}

func (m *memPresence) Online(_ context.Context, users []string) (map[string]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for _, u := range users {
		out[u] = len(m.conns[u]) > 0
	}
	return out, nil
}

type api struct {
	t       *testing.T
	srv     *httptest.Server
	pool    *pgxpool.Pool
	userIDs []string
}

func newAPI(t *testing.T) *api {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Fatal("DATABASE_URL must be set for integration tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	store := postgres.NewStore(pool)
	hasher := auth.NewHasher(auth.Params{Memory: 8, Time: 1, Threads: 1, KeyLen: 32, SaltLen: 16}, 4) // fast, tests only
	tokens, err := auth.NewTokenIssuer([]byte(strings.Repeat("k", 48)), "it", 15*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(store, hasher, tokens, accounts.Config{RefreshTTL: time.Hour, SessionMaxLifetime: 24 * time.Hour, MaxDevices: 20}, logger)
	if err != nil {
		t.Fatal(err)
	}
	presence := &memPresence{}
	wsCfg := ws.DefaultConfig()
	wsCfg.PingInterval = time.Hour
	wsCfg.RevalidateInterval = 100 * time.Millisecond // so revocation is observed quickly in tests
	hub := ws.NewHub(wsCfg, ws.Deps{Sessions: store, Presence: presence, Tokens: tokens, Logger: logger})
	msgSvc := messages.New(store, hub)
	hub.Bind(msgSvc)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hub.Shutdown(ctx)
	})
	s := httpapi.New(httpapi.Deps{Accounts: acc, Chats: chats.New(store), Messages: msgSvc, Presence: presence, WS: hub,
		Tokens: tokens, Limiter: ratelimit.NewMemory(nil), Logger: logger})
	mux := http.NewServeMux()
	s.Register(mux)
	mux.HandleFunc("/", httpx.NotFound)
	srv := httptest.NewServer(httpx.Chain(mux, httpx.RequestID, httpx.Recover(logger)))
	t.Cleanup(srv.Close)

	a := &api{t: t, srv: srv, pool: pool}
	t.Cleanup(func() { // registered before the pool closes, so it runs first
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM conversations WHERE id IN (
			SELECT conversation_id FROM conversation_members WHERE user_id = ANY($1::text[]::uuid[]))`, a.userIDs)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1::text[]::uuid[])`, a.userIDs)
	})
	return a
}

type resp struct {
	Status int
	Body   map[string]any
	Header http.Header
}

func (a *api) do(method, path, token string, body any) resp {
	a.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.srv.URL+path, rdr)
	if err != nil {
		a.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Header: res.Header}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.Body)
	}
	return out
}

func (r resp) str(keys ...string) string {
	var cur any = r.Body
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[k]
	}
	s, _ := cur.(string)
	return s
}

func (a *api) expect(r resp, status int, what string) resp {
	a.t.Helper()
	if r.Status != status {
		a.t.Fatalf("%s: want HTTP %d, got %d (%v)", what, status, r.Status, r.Body)
	}
	return r
}

type person struct {
	ID, Email, Access, Refresh, DeviceID, SessionID string
}

func (a *api) signup(name string) person {
	a.t.Helper()
	email := fmt.Sprintf("%s-%d@example.test", name, time.Now().UnixNano())
	pw := "a-long-test-password"
	reg := a.expect(a.do("POST", "/v1/auth/register", "", map[string]any{"email": email, "password": pw, "display_name": name}), 201, "register")
	id := reg.str("user", "id")
	a.userIDs = append(a.userIDs, id)
	lg := a.expect(a.do("POST", "/v1/auth/login", "", map[string]any{
		"email": email, "password": pw, "device": map[string]any{"name": name + "-laptop", "platform": "web"}}), 200, "login")
	return person{ID: id, Email: email, Access: lg.str("access_token"), Refresh: lg.str("refresh_token"), DeviceID: lg.str("device_id"), SessionID: lg.str("session_id")}
}

// ---- authentication --------------------------------------------------------------------------

func TestAuthLifecycle(t *testing.T) {
	a := newAPI(t)
	email := fmt.Sprintf("auth-%d@example.test", time.Now().UnixNano())
	good := map[string]any{"email": email, "password": "a-long-test-password", "display_name": "Auth"}

	a.expect(a.do("POST", "/v1/auth/register", "", map[string]any{"email": email, "password": "short", "display_name": "x"}), 400, "weak password")
	reg := a.expect(a.do("POST", "/v1/auth/register", "", good), 201, "register")
	a.userIDs = append(a.userIDs, reg.str("user", "id"))
	a.expect(a.do("POST", "/v1/auth/register", "", good), 409, "duplicate email")
	if reg.str("user", "password_hash") != "" || reg.str("user", "password") != "" {
		t.Error("password material must never be returned")
	}

	dev := map[string]any{"name": "Phone", "platform": "ios"}
	wrong := a.expect(a.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "wrong-password-here", "device": dev}), 401, "wrong password")
	unknown := a.expect(a.do("POST", "/v1/auth/login", "", map[string]any{"email": "nobody-" + email, "password": "wrong-password-here", "device": dev}), 401, "unknown email")
	if wrong.str("message") != unknown.str("message") || wrong.str("code") != unknown.str("code") {
		t.Errorf("login failures must be indistinguishable: %q vs %q", wrong.str("message"), unknown.str("message"))
	}

	lg := a.expect(a.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "a-long-test-password", "device": dev}), 200, "login")
	access, refresh := lg.str("access_token"), lg.str("refresh_token")

	a.expect(a.do("GET", "/v1/me", "", nil), 401, "no token")
	a.expect(a.do("GET", "/v1/me", "garbage", nil), 401, "bad token")
	me := a.expect(a.do("GET", "/v1/me", access, nil), 200, "me")
	if me.str("user", "email") != email {
		t.Errorf("me = %v", me.Body)
	}
	upd := a.expect(a.do("PATCH", "/v1/me", access, map[string]any{"display_name": "Renamed"}), 200, "update me")
	if upd.str("user", "display_name") != "Renamed" {
		t.Errorf("update = %v", upd.Body)
	}

	// Rotation: new tokens differ; replaying the OLD refresh token revokes the whole session.
	r1 := a.expect(a.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": refresh}), 200, "refresh")
	newRefresh := r1.str("refresh_token")
	if newRefresh == "" || newRefresh == refresh {
		t.Fatal("refresh token must rotate")
	}
	a.expect(a.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": refresh}), 401, "reuse of rotated token")
	a.expect(a.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": newRefresh}), 401, "family revoked after reuse")
	a.expect(a.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": "definitely-not-a-real-token"}), 401, "unknown refresh token")

	// Logout kills the session's refresh tokens.
	lg2 := a.expect(a.do("POST", "/v1/auth/login", "", map[string]any{"email": email, "password": "a-long-test-password", "device": dev}), 200, "second login")
	a.expect(a.do("POST", "/v1/auth/logout", lg2.str("access_token"), nil), 204, "logout")
	a.expect(a.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": lg2.str("refresh_token")}), 401, "refresh after logout")
}

func TestSessionsAndDevices(t *testing.T) {
	a := newAPI(t)
	p := a.signup("sess")
	dev := map[string]any{"id": p.DeviceID, "name": "ignored", "platform": "web"}
	second := a.expect(a.do("POST", "/v1/auth/login", "", map[string]any{"email": p.Email, "password": "a-long-test-password", "device": dev}), 200, "login on same device")
	if second.str("device_id") != p.DeviceID {
		t.Fatal("existing device id must be reused")
	}

	list := a.expect(a.do("GET", "/v1/sessions", p.Access, nil), 200, "list sessions")
	sessions, _ := list.Body["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}
	a.expect(a.do("DELETE", "/v1/sessions/"+second.str("session_id"), p.Access, nil), 204, "revoke other session")
	a.expect(a.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": second.str("refresh_token")}), 401, "revoked session cannot refresh")
	a.expect(a.do("DELETE", "/v1/sessions/not-a-uuid", p.Access, nil), 404, "malformed session id")

	other := a.signup("sess-other")
	a.expect(a.do("DELETE", "/v1/sessions/"+p.SessionID, other.Access, nil), 404, "another user's session is invisible")
	a.expect(a.do("DELETE", "/v1/devices/"+p.DeviceID, other.Access, nil), 404, "another user's device is invisible")

	devs := a.expect(a.do("GET", "/v1/devices", p.Access, nil), 200, "list devices")
	if d, _ := devs.Body["devices"].([]any); len(d) != 1 {
		t.Fatalf("want 1 device, got %d", len(d))
	}
	a.expect(a.do("DELETE", "/v1/devices/"+p.DeviceID, p.Access, nil), 204, "revoke device")
	a.expect(a.do("POST", "/v1/auth/refresh", "", map[string]any{"refresh_token": p.Refresh}), 401, "refresh after device revoked")
}

func TestLoginRateLimit(t *testing.T) {
	a := newAPI(t)
	email := fmt.Sprintf("rl-%d@example.test", time.Now().UnixNano())
	body := map[string]any{"email": email, "password": "wrong-password-here", "device": map[string]any{"name": "d", "platform": "web"}}
	for i := 0; i < 10; i++ {
		a.expect(a.do("POST", "/v1/auth/login", "", body), 401, fmt.Sprintf("attempt %d", i+1))
	}
	r := a.expect(a.do("POST", "/v1/auth/login", "", body), 429, "11th attempt for the same email")
	if r.Header.Get("Retry-After") == "" || r.str("code") != "rate_limited" {
		t.Errorf("429 response: %v headers=%v", r.Body, r.Header)
	}
}

// ---- authorization: the Phase 2 acceptance criterion ------------------------------------------

func TestUsersCannotAccessConversationsTheyDoNotBelongTo(t *testing.T) {
	a := newAPI(t)
	alice, bob, carol := a.signup("alice"), a.signup("bob"), a.signup("carol")

	g := a.expect(a.do("POST", "/v1/conversations/groups", alice.Access, map[string]any{"title": "Team", "member_ids": []string{bob.ID}}), 201, "create group")
	gid := g.str("conversation", "id")
	if g.str("conversation", "role") != "admin" {
		t.Errorf("creator should be admin: %v", g.Body)
	}
	path := "/v1/conversations/" + gid

	// Carol is a stranger: every endpoint answers 404, indistinguishable from a missing id.
	missing := a.expect(a.do("GET", "/v1/conversations/00000000-0000-0000-0000-00000000dead", carol.Access, nil), 404, "missing conversation")
	for name, rq := range map[string]struct{ m, p string; body any }{
		"get":      {"GET", path, nil},
		"rename":   {"PATCH", path, map[string]any{"title": "pwned"}},
		"members":  {"GET", path + "/members", nil},
		"add self": {"POST", path + "/members", map[string]any{"user_ids": []string{carol.ID}}},
		"remove":   {"DELETE", path + "/members/" + bob.ID, nil},
		"role":     {"PUT", path + "/members/" + bob.ID + "/role", map[string]any{"role": "admin"}},
		"settings": {"GET", path + "/settings", nil},
		"ai":       {"PATCH", path + "/settings", map[string]any{"ai_enabled": true}},
	} {
		r := a.expect(a.do(rq.m, rq.p, carol.Access, rq.body), 404, "stranger: "+name)
		if r.str("code") != missing.str("code") || r.str("message") != missing.str("message") {
			t.Errorf("%s: response must be identical to a missing conversation", name)
		}
	}
	// Nothing changed.
	g2 := a.expect(a.do("GET", path, alice.Access, nil), 200, "admin get")
	if g2.str("conversation", "title") != "Team" || g2.Body["conversation"].(map[string]any)["member_count"].(float64) != 2 {
		t.Errorf("stranger's requests must not modify the group: %v", g2.Body)
	}

	// Bob is a plain member: he can read, but not administer.
	a.expect(a.do("GET", path, bob.Access, nil), 200, "member get")
	a.expect(a.do("GET", path+"/members", bob.Access, nil), 200, "member list")
	a.expect(a.do("PATCH", path, bob.Access, map[string]any{"title": "mine now"}), 403, "member rename")
	a.expect(a.do("POST", path+"/members", bob.Access, map[string]any{"user_ids": []string{carol.ID}}), 403, "member add")
	a.expect(a.do("DELETE", path+"/members/"+alice.ID, bob.Access, nil), 403, "member removes admin")
	a.expect(a.do("PUT", path+"/members/"+bob.ID+"/role", bob.Access, map[string]any{"role": "admin"}), 403, "self-promotion")
	a.expect(a.do("PATCH", path+"/settings", bob.Access, map[string]any{"ai_enabled": true}), 403, "member enables AI")

	// Admin adds Carol: she now has access. Admin removes her: access disappears again.
	a.expect(a.do("POST", path+"/members", alice.Access, map[string]any{"user_ids": []string{carol.ID}}), 200, "admin adds carol")
	a.expect(a.do("GET", path, carol.Access, nil), 200, "carol after being added")
	a.expect(a.do("DELETE", path+"/members/"+carol.ID, alice.Access, nil), 204, "admin removes carol")
	a.expect(a.do("GET", path, carol.Access, nil), 404, "carol after being removed")
	a.expect(a.do("GET", path+"/members", carol.Access, nil), 404, "removed member cannot list members")

	// Members may leave; the last admin leaving promotes someone.
	a.expect(a.do("DELETE", path+"/members/"+alice.ID, alice.Access, nil), 204, "admin leaves")
	a.expect(a.do("GET", path, alice.Access, nil), 404, "alice after leaving")
	b := a.expect(a.do("GET", path, bob.Access, nil), 200, "bob still a member")
	if b.str("conversation", "role") != "admin" {
		t.Errorf("remaining member should have been promoted: %v", b.Body)
	}
}

func TestDirectConversations(t *testing.T) {
	a := newAPI(t)
	alice, bob, carol := a.signup("alice"), a.signup("bob"), a.signup("carol")

	c1 := a.expect(a.do("POST", "/v1/conversations/direct", alice.Access, map[string]any{"user_id": bob.ID}), 201, "create direct")
	c2 := a.expect(a.do("POST", "/v1/conversations/direct", bob.Access, map[string]any{"user_id": alice.ID}), 200, "same pair from the other side")
	if c1.str("conversation", "id") != c2.str("conversation", "id") {
		t.Fatal("one direct conversation per pair")
	}
	id := c1.str("conversation", "id")
	a.expect(a.do("GET", "/v1/conversations/"+id, carol.Access, nil), 404, "third party cannot read a direct conversation")
	a.expect(a.do("POST", "/v1/conversations/"+id+"/members", alice.Access, map[string]any{"user_ids": []string{carol.ID}}), 403, "direct conversations have no admins")
	a.expect(a.do("DELETE", "/v1/conversations/"+id+"/members/"+bob.ID, alice.Access, nil), 403, "cannot remove from a direct conversation")
	a.expect(a.do("POST", "/v1/conversations/direct", alice.Access, map[string]any{"user_id": alice.ID}), 400, "self conversation")
	a.expect(a.do("POST", "/v1/conversations/direct", alice.Access, map[string]any{"user_id": "00000000-0000-0000-0000-00000000dead"}), 404, "unknown user")

	list := a.expect(a.do("GET", "/v1/conversations", alice.Access, nil), 200, "alice list")
	items, _ := list.Body["conversations"].([]any)
	if len(items) != 1 {
		t.Fatalf("alice should see 1 conversation, got %d", len(items))
	}
	peer := items[0].(map[string]any)["peer"].(map[string]any)
	if peer["user_id"] != bob.ID {
		t.Errorf("peer should be bob: %v", peer)
	}
	if l := a.expect(a.do("GET", "/v1/conversations", carol.Access, nil), 200, "carol list"); len(l.Body["conversations"].([]any)) != 0 {
		t.Error("carol must not see other people's conversations")
	}

	found := a.expect(a.do("GET", "/v1/users/lookup?email="+bob.Email, alice.Access, nil), 200, "lookup")
	if found.str("user", "id") != bob.ID || found.str("user", "email") != "" {
		t.Errorf("lookup must return id and name only: %v", found.Body)
	}
	a.expect(a.do("GET", "/v1/users/lookup?email=nobody@example.test", alice.Access, nil), 404, "lookup unknown")
}

func TestConversationListPagination(t *testing.T) {
	a := newAPI(t)
	alice, bob := a.signup("alice"), a.signup("bob")
	for i := 0; i < 3; i++ {
		a.expect(a.do("POST", "/v1/conversations/groups", alice.Access, map[string]any{"title": fmt.Sprintf("G%d", i), "member_ids": []string{bob.ID}}), 201, "create group")
		time.Sleep(3 * time.Millisecond)
	}
	p1 := a.expect(a.do("GET", "/v1/conversations?limit=2", alice.Access, nil), 200, "page 1")
	cur := p1.str("next_cursor")
	if len(p1.Body["conversations"].([]any)) != 2 || cur == "" {
		t.Fatalf("page 1: %v", p1.Body)
	}
	p2 := a.expect(a.do("GET", "/v1/conversations?limit=2&cursor="+cur, alice.Access, nil), 200, "page 2")
	if len(p2.Body["conversations"].([]any)) != 1 || p2.Body["next_cursor"] != nil {
		t.Fatalf("page 2: %v", p2.Body)
	}
	a.expect(a.do("GET", "/v1/conversations?cursor=garbage", alice.Access, nil), 400, "bad cursor")
	a.expect(a.do("GET", "/v1/conversations?limit=0", alice.Access, nil), 400, "bad limit")
}

func TestGroupValidation(t *testing.T) {
	a := newAPI(t)
	alice, bob := a.signup("alice"), a.signup("bob")
	for name, body := range map[string]map[string]any{
		"no members":    {"title": "T", "member_ids": []string{}},
		"blank title":   {"title": "  ", "member_ids": []string{bob.ID}},
		"unknown user":  {"title": "T", "member_ids": []string{"00000000-0000-0000-0000-00000000dead"}},
		"malformed id":  {"title": "T", "member_ids": []string{"nope"}},
		"unknown field": {"title": "T", "member_ids": []string{bob.ID}, "admin": true},
	} {
		a.expect(a.do("POST", "/v1/conversations/groups", alice.Access, body), 400, name)
	}
}
