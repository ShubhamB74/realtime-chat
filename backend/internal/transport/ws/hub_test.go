package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"realtime-chat/backend/internal/app/messages"
	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
)

const (
	conv1 = "11111111-1111-1111-1111-111111111111"
	boom  = "99999999-9999-9999-9999-999999999999"
)

// ---- fakes -----------------------------------------------------------------------------------

type fakeTokens map[string]auth.Identity

func (f fakeTokens) Verify(raw string) (auth.Identity, error) {
	id, ok := f[raw]
	if !ok {
		return auth.Identity{}, auth.ErrInvalidToken
	}
	return id, nil
}

type fakeSessions struct {
	mu       sync.Mutex
	inactive map[string]bool
}

func (f *fakeSessions) SessionActive(_ context.Context, _, family, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.inactive[family], nil
}

func (f *fakeSessions) revoke(family string) {
	f.mu.Lock()
	f.inactive[family] = true
	f.mu.Unlock()
}

type fakePresence struct {
	mu      sync.Mutex
	touched map[string]int
	removed map[string]int
}

func (f *fakePresence) Touch(_ context.Context, user, _ string, _ time.Duration) error {
	f.mu.Lock()
	f.touched[user]++
	f.mu.Unlock()
	return nil
}
func (f *fakePresence) Remove(_ context.Context, user, _ string) error {
	f.mu.Lock()
	f.removed[user]++
	f.mu.Unlock()
	return nil
}
func (f *fakePresence) count(m map[string]int, user string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return m[user]
}

// fakeMessages mimics the real service: it stores, then publishes to the hub.
type fakeMessages struct {
	hub     *Hub
	mu      sync.Mutex
	members map[string][]string
	log     map[string][]domain.Message
	byKey   map[string]domain.Message
}

func (f *fakeMessages) isMember(conv, user string) bool {
	for _, m := range f.members[conv] {
		if m == user {
			return true
		}
	}
	return false
}

func (f *fakeMessages) Send(_ context.Context, in messages.SendInput) (messages.SendOutput, error) {
	if in.ConversationID == boom {
		panic("handler exploded")
	}
	f.mu.Lock()
	if !f.isMember(in.ConversationID, in.UserID) {
		f.mu.Unlock()
		return messages.SendOutput{}, domain.NotFound()
	}
	key := in.DeviceID + "/" + in.IdempotencyKey
	if m, ok := f.byKey[key]; ok {
		f.mu.Unlock()
		return messages.SendOutput{Message: m, Duplicate: true}, nil
	}
	seq := int64(len(f.log[in.ConversationID]) + 1)
	uid, body := in.UserID, in.Body
	msg := domain.Message{ID: "m" + strings.Repeat("0", 3) + string(rune('a'+seq)), ConversationID: in.ConversationID,
		SenderID: &uid, Seq: seq, ModSeq: seq, Body: &body, CreatedAt: time.Now(), IdempotencyKey: in.IdempotencyKey}
	f.log[in.ConversationID] = append(f.log[in.ConversationID], msg)
	f.byKey[key] = msg
	recips := append([]string(nil), f.members[in.ConversationID]...)
	f.mu.Unlock()
	f.hub.Publish(messages.Event{Type: messages.EventCreated, Recipients: recips, Message: msg, OriginConn: in.OriginConn})
	return messages.SendOutput{Message: msg}, nil
}

func (f *fakeMessages) Changes(_ context.Context, user, conv string, after int64, limit int) (domain.Changes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.isMember(conv, user) {
		return domain.Changes{}, domain.NotFound()
	}
	var out []domain.Message
	for _, m := range f.log[conv] {
		if m.ModSeq > after {
			out = append(out, m)
		}
	}
	ch := domain.Changes{LastSeq: int64(len(f.log[conv]))}
	if len(out) > limit {
		ch.HasMore, out = true, out[:limit]
	}
	ch.Messages = out
	return ch, nil
}

func (f *fakeMessages) Recipients(_ context.Context, user, conv string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.isMember(conv, user) {
		return nil, domain.NotFound()
	}
	return append([]string(nil), f.members[conv]...), nil
}

// ---- harness ---------------------------------------------------------------------------------

type testEnv struct {
	hub      *Hub
	srv      *httptest.Server
	msgs     *fakeMessages
	sessions *fakeSessions
	presence *fakePresence
}

func newEnv(t *testing.T, mutate func(*Config)) *testEnv {
	t.Helper()
	cfg := DefaultConfig()
	cfg.PingInterval = time.Hour // heartbeats are not exercised here
	cfg.RevalidateInterval = time.Hour
	if mutate != nil {
		mutate(&cfg)
	}
	tokens := fakeTokens{
		"tok-alice": {UserID: "ua", SessionID: "sa", DeviceID: "da"},
		"tok-bob":   {UserID: "ub", SessionID: "sb", DeviceID: "db"},
		"tok-carol": {UserID: "uc", SessionID: "sc", DeviceID: "dc"},
	}
	e := &testEnv{
		sessions: &fakeSessions{inactive: map[string]bool{}},
		presence: &fakePresence{touched: map[string]int{}, removed: map[string]int{}},
	}
	e.hub = NewHub(cfg, Deps{Sessions: e.sessions, Presence: e.presence, Tokens: tokens, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	e.msgs = &fakeMessages{hub: e.hub, members: map[string][]string{conv1: {"ua", "ub"}}, log: map[string][]domain.Message{}, byKey: map[string]domain.Message{}}
	e.hub.Bind(e.msgs)
	e.srv = httptest.NewServer(e.hub)
	t.Cleanup(e.srv.Close)
	return e
}

func (e *testEnv) url() string { return "ws" + strings.TrimPrefix(e.srv.URL, "http") }

func (e *testEnv) dialOpts(t *testing.T, opts *websocket.DialOptions) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return websocket.Dial(ctx, e.url(), opts)
}

// dial connects with the Authorization header and consumes the connection.ready event.
func (e *testEnv) dial(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	h := http.Header{}
	h.Set("Authorization", "Bearer "+token)
	c, _, err := e.dialOpts(t, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("dial %s: %v", token, err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	if ev := readEnv(t, c); ev.Type != EvReady {
		t.Fatalf("first event = %q, want %q", ev.Type, EvReady)
	}
	return c
}

func readEnv(t *testing.T, c *websocket.Conn) Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatalf("bad frame %q: %v", data, err)
	}
	return env
}

// expectSilence asserts nothing arrives within d. Call it last on a connection: a read that
// times out closes the connection.
func expectSilence(t *testing.T, c *websocket.Conn, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if _, data, err := c.Read(ctx); err == nil {
		t.Fatalf("unexpected frame: %s", data)
	}
}

func write(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func event(typ, reqID string, payload any) map[string]any {
	return map[string]any{"v": 1, "type": typ, "request_id": reqID, "payload": payload}
}

func sendEv(reqID, conv, key, body string) map[string]any {
	return event(EvMessageSend, reqID, map[string]any{"conversation_id": conv, "idempotency_key": key, "body": body})
}

func payloadOf(t *testing.T, env Envelope, dst any) {
	t.Helper()
	if err := json.Unmarshal(env.Payload, dst); err != nil {
		t.Fatalf("payload: %v", err)
	}
}

func errCode(t *testing.T, env Envelope) string {
	t.Helper()
	if env.Type != EvError {
		t.Fatalf("want error event, got %q (%s)", env.Type, env.Payload)
	}
	var p errorPayload
	payloadOf(t, env, &p)
	return p.Code
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// ---- tests -----------------------------------------------------------------------------------

func TestHandshakeAuthentication(t *testing.T) {
	e := newEnv(t, nil)

	if _, resp, err := e.dialOpts(t, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: want HTTP 401, got err=%v resp=%v", err, resp)
	}
	h := http.Header{"Authorization": {"Bearer nope"}}
	if _, resp, err := e.dialOpts(t, &websocket.DialOptions{HTTPHeader: h}); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: want HTTP 401, got err=%v resp=%v", err, resp)
	}
	// A valid token for a revoked session is refused too.
	e.sessions.revoke("sc")
	h = http.Header{"Authorization": {"Bearer tok-carol"}}
	if _, resp, err := e.dialOpts(t, &websocket.DialOptions{HTTPHeader: h}); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session: want HTTP 401, got err=%v resp=%v", err, resp)
	}
	// Query-string tokens are not accepted.
	c, resp, err := websocket.Dial(context.Background(), e.url()+"?access_token=tok-alice", nil)
	if err == nil {
		_ = c.CloseNow()
		t.Fatal("query-string token must not authenticate")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("query token: want 401, got %v", resp)
	}

	// Browser style: token as a second offered subprotocol.
	c2, _, err := e.dialOpts(t, &websocket.DialOptions{Subprotocols: []string{Subprotocol, "bearer.tok-alice"}})
	if err != nil {
		t.Fatalf("subprotocol auth: %v", err)
	}
	defer func() { _ = c2.CloseNow() }()
	if c2.Subprotocol() != Subprotocol {
		t.Errorf("negotiated subprotocol = %q", c2.Subprotocol())
	}
	if ev := readEnv(t, c2); ev.Type != EvReady {
		t.Errorf("first event = %q", ev.Type)
	}
}

func TestReadyEventAnnouncesLimits(t *testing.T) {
	e := newEnv(t, nil)
	h := http.Header{"Authorization": {"Bearer tok-alice"}}
	c, _, err := e.dialOpts(t, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	ev := readEnv(t, c)
	var p readyPayload
	payloadOf(t, ev, &p)
	if ev.V != 1 || ev.EventID == "" || ev.TS == "" || p.UserID != "ua" || p.DeviceID != "da" || p.MaxFrameBytes != 32<<10 || p.ConnectionID == "" {
		t.Errorf("ready = %+v / %+v", ev, p)
	}
}

func TestSendIsAcknowledgedAndDeliveredToOtherMembersOnly(t *testing.T) {
	e := newEnv(t, nil)
	alice, bob, bob2, carol := e.dial(t, "tok-alice"), e.dial(t, "tok-bob"), e.dial(t, "tok-bob"), e.dial(t, "tok-carol")

	write(t, alice, sendEv("r1", conv1, "key-00000001", "hello bob"))

	acc := readEnv(t, alice)
	if acc.Type != EvAccepted || acc.RequestID != "r1" {
		t.Fatalf("accepted = %+v", acc)
	}
	var ap acceptedPayload
	payloadOf(t, acc, &ap)
	if ap.Duplicate || ap.Message.Seq != 1 || ap.Message.IdempotencyKey != "key-00000001" || ap.Message.Body == nil || *ap.Message.Body != "hello bob" {
		t.Errorf("accepted payload = %+v", ap)
	}

	for name, c := range map[string]*websocket.Conn{"bob device 1": bob, "bob device 2": bob2} {
		ev := readEnv(t, c)
		if ev.Type != EvReceived {
			t.Fatalf("%s: got %q", name, ev.Type)
		}
		var msg struct {
			ID   string  `json:"id"`
			Seq  int64   `json:"seq"`
			Body *string `json:"body"`
		}
		payloadOf(t, ev, &msg)
		if msg.Seq != 1 || msg.Body == nil || *msg.Body != "hello bob" {
			t.Errorf("%s: message = %+v", name, msg)
		}
	}
	expectSilence(t, carol, 200*time.Millisecond) // not a member
	expectSilence(t, alice, 200*time.Millisecond) // the origin connection gets only the ack
}

func TestDuplicateSendIsIdempotentAndNotRebroadcast(t *testing.T) {
	e := newEnv(t, nil)
	alice, bob := e.dial(t, "tok-alice"), e.dial(t, "tok-bob")
	write(t, alice, sendEv("r1", conv1, "key-00000002", "once"))
	write(t, alice, sendEv("r2", conv1, "key-00000002", "once"))

	first, second := readEnv(t, alice), readEnv(t, alice)
	var p1, p2 acceptedPayload
	payloadOf(t, first, &p1)
	payloadOf(t, second, &p2)
	if p1.Duplicate || !p2.Duplicate || p1.Message.ID != p2.Message.ID || p1.Message.Seq != p2.Message.Seq {
		t.Fatalf("p1=%+v p2=%+v", p1, p2)
	}
	if ev := readEnv(t, bob); ev.Type != EvReceived {
		t.Fatalf("bob: %q", ev.Type)
	}
	expectSilence(t, bob, 250*time.Millisecond) // exactly one delivery
}

func TestNonMemberCannotSendAndConnectionSurvives(t *testing.T) {
	e := newEnv(t, nil)
	carol := e.dial(t, "tok-carol")
	write(t, carol, sendEv("r1", conv1, "key-00000003", "let me in"))
	ev := readEnv(t, carol)
	if ev.RequestID != "r1" || errCode(t, ev) != "not_found" {
		t.Fatalf("event = %+v", ev)
	}
	e.msgs.mu.Lock()
	n := len(e.msgs.log[conv1])
	e.msgs.mu.Unlock()
	if n != 0 {
		t.Error("nothing may be stored for a non-member")
	}
	// Still usable.
	write(t, carol, event(EvSyncRequest, "r2", map[string]any{"conversations": []map[string]any{{"conversation_id": conv1, "after_mod_seq": 0}}}))
	var b syncBatch
	ev = readEnv(t, carol)
	payloadOf(t, ev, &b)
	if ev.Type != EvSyncBatch || b.Status != "not_found" || len(b.Changes) != 0 || !b.Done {
		t.Fatalf("sync for a non-member: %+v", b)
	}
}

func TestBadFramesGetErrorsButKeepTheConnection(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t, "tok-alice")
	ctx := context.Background()

	_ = c.Write(ctx, websocket.MessageText, []byte("not json"))
	if got := errCode(t, readEnv(t, c)); got != "invalid_request" {
		t.Errorf("malformed json: %s", got)
	}
	write(t, c, map[string]any{"v": 2, "type": EvMessageSend, "request_id": "r1"})
	if ev := readEnv(t, c); errCode(t, ev) != "unsupported_version" || ev.RequestID != "r1" {
		t.Errorf("version: %+v", ev)
	}
	write(t, c, event("nonsense.event", "r2", nil))
	if got := errCode(t, readEnv(t, c)); got != "unsupported_event" {
		t.Errorf("unknown event: %s", got)
	}
	write(t, c, event("message.ack", "r3", map[string]any{"conversation_id": conv1, "up_to_seq": 1}))
	ev := readEnv(t, c)
	var p errorPayload
	payloadOf(t, ev, &p)
	if p.Code != "unsupported_event" || !strings.Contains(p.Message, "Phase 4A") {
		t.Errorf("planned event: %+v", p)
	}
	write(t, c, event(EvMessageSend, "r4", map[string]any{"conversation_id": conv1, "idempotency_key": "key-00000004", "body": "x", "unknown_field": 1}))
	if got := errCode(t, readEnv(t, c)); got != "invalid_request" {
		t.Errorf("unknown payload field: %s", got)
	}
	write(t, c, sendEv("r5", conv1, "short", "x"))
	if got := errCode(t, readEnv(t, c)); got != "invalid_request" {
		t.Errorf("short idempotency key must be rejected by the service layer, got %s", got)
	}
}

func TestBinaryFramesCloseTheConnection(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t, "tok-alice")
	_ = c.Write(context.Background(), websocket.MessageBinary, []byte{1, 2, 3})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusUnsupportedData {
		t.Fatalf("close status = %v (err=%v)", websocket.CloseStatus(err), err)
	}
}

func TestOversizedFrameClosesTheConnection(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.ReadLimit = 1024 })
	c := e.dial(t, "tok-alice")
	_ = c.Write(context.Background(), websocket.MessageText, []byte(strings.Repeat("x", 4096)))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("close status = %v (err=%v)", websocket.CloseStatus(err), err)
	}
}

func TestSyncReturnsChangesAfterCursor(t *testing.T) {
	e := newEnv(t, nil)
	alice := e.dial(t, "tok-alice")
	for i, key := range []string{"key-aaaaaaa1", "key-aaaaaaa2", "key-aaaaaaa3"} {
		write(t, alice, sendEv("s"+string(rune('0'+i)), conv1, key, "m"))
		readEnv(t, alice) // accepted
	}
	write(t, alice, event(EvSyncRequest, "sync1", map[string]any{"conversations": []map[string]any{
		{"conversation_id": conv1, "after_mod_seq": 1},
		{"conversation_id": "22222222-2222-2222-2222-222222222222", "after_mod_seq": 0},
	}}))
	first, second := readEnv(t, alice), readEnv(t, alice)
	var b1, b2 syncBatch
	payloadOf(t, first, &b1)
	payloadOf(t, second, &b2)
	if first.RequestID != "sync1" || b1.Status != "ok" || len(b1.Changes) != 2 || b1.Changes[0].ModSeq != 2 || b1.LastSeq != 3 || b1.Done {
		t.Errorf("first batch: %+v", b1)
	}
	if b2.Status != "not_found" || !b2.Done {
		t.Errorf("second batch: %+v", b2)
	}

	time.Sleep(1100 * time.Millisecond) // sync requests are limited to one per second
	write(t, alice, event(EvSyncRequest, "sync2", map[string]any{"conversations": []map[string]any{}}))
	if got := errCode(t, readEnv(t, alice)); got != "invalid_request" {
		t.Errorf("empty sync: %s", got)
	}
}

func TestTypingIndicators(t *testing.T) {
	e := newEnv(t, nil)
	alice, bob, carol := e.dial(t, "tok-alice"), e.dial(t, "tok-bob"), e.dial(t, "tok-carol")

	write(t, alice, event(EvTypingStart, "", map[string]any{"conversation_id": conv1}))
	ev := readEnv(t, bob)
	var tu typingUpdate
	payloadOf(t, ev, &tu)
	if ev.Type != EvTyping || tu.UserID != "ua" || !tu.Typing || tu.ConversationID != conv1 {
		t.Fatalf("typing update = %+v / %+v", ev, tu)
	}

	write(t, carol, event(EvTypingStart, "t2", map[string]any{"conversation_id": conv1}))
	if got := errCode(t, readEnv(t, carol)); got != "not_found" {
		t.Errorf("non-member typing: %s", got)
	}
	expectSilence(t, alice, 200*time.Millisecond) // the typist is not told about their own typing
}

func TestSessionRevocationClosesTheConnection(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.RevalidateInterval = 30 * time.Millisecond })
	c := e.dial(t, "tok-alice")
	e.sessions.revoke("sa")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if websocket.CloseStatus(err) != CloseSessionRevoked {
		t.Fatalf("close status = %v (err=%v), want %d", websocket.CloseStatus(err), err, CloseSessionRevoked)
	}
}

func TestSlowConsumerIsDisconnectedNotBuffered(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.SendQueue = 4 })
	_ = e.dial(t, "tok-bob") // connects, then never reads again

	body := strings.Repeat("x", 256<<10)
	for i := 0; i < 400; i++ {
		e.hub.Publish(messages.Event{
			Type: messages.EventCreated, Recipients: []string{"ub"},
			Message: domain.Message{ID: "big", ConversationID: conv1, Seq: int64(i + 1), ModSeq: int64(i + 1), Body: &body},
		})
	}
	waitFor(t, "slow consumer to be disconnected", func() bool { return e.hub.Stats().SlowConsumers >= 1 })
}

func TestConnectionLimits(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.MaxConnsPerUser = 2; c.MaxConns = 3 })
	a1 := e.dial(t, "tok-alice")
	_ = e.dial(t, "tok-alice")

	h := http.Header{"Authorization": {"Bearer tok-alice"}}
	if _, resp, err := e.dialOpts(t, &websocket.DialOptions{HTTPHeader: h}); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("per-user limit: want 429, got err=%v resp=%v", err, resp)
	}
	_ = e.dial(t, "tok-bob") // third connection overall
	h = http.Header{"Authorization": {"Bearer tok-carol"}}
	if _, resp, err := e.dialOpts(t, &websocket.DialOptions{HTTPHeader: h}); err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("global limit: want 503, got err=%v resp=%v", err, resp)
	}
	if e.hub.Stats().RejectedLimit != 2 {
		t.Errorf("rejected = %d", e.hub.Stats().RejectedLimit)
	}

	// Slots are released when a connection closes.
	_ = a1.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "slot to be released", func() bool {
		c, _, err := e.dialOpts(t, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer tok-alice"}}})
		if err == nil {
			_ = c.CloseNow()
		}
		return err == nil
	})
}

func TestGracefulShutdownSaysGoingAway(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t, "tok-alice")

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- e.hub.Shutdown(ctx)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := c.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusGoingAway {
		t.Fatalf("close status = %v (err=%v)", websocket.CloseStatus(err), err)
	}
	if err := <-done; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	h := http.Header{"Authorization": {"Bearer tok-bob"}}
	if _, resp, err := e.dialOpts(t, &websocket.DialOptions{HTTPHeader: h}); err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("after shutdown: want 503, got err=%v resp=%v", err, resp)
	}
}

func TestPresenceIsRecordedAndRemoved(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t, "tok-alice")
	if e.presence.count(e.presence.touched, "ua") < 1 {
		t.Error("presence should be touched on connect")
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
	waitFor(t, "presence removal", func() bool { return e.presence.count(e.presence.removed, "ua") >= 1 })
}

func TestInboundRateLimit(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.EventRate = 0.001; c.EventBurst = 2 })
	c := e.dial(t, "tok-alice")
	for i := 0; i < 5; i++ {
		write(t, c, event("nonsense.event", "", nil))
	}
	codes := map[string]int{}
	for i := 0; i < 5; i++ {
		codes[errCode(t, readEnv(t, c))]++
	}
	if codes["unsupported_event"] != 2 || codes["rate_limited"] != 3 {
		t.Errorf("codes = %v, want 2 handled and 3 rate limited", codes)
	}
}

func TestPanicInHandlerClosesOnlyThatConnection(t *testing.T) {
	e := newEnv(t, nil)
	victim, bystander := e.dial(t, "tok-carol"), e.dial(t, "tok-alice")
	write(t, victim, sendEv("r1", boom, "key-00000009", "boom"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := victim.Read(ctx)
	if websocket.CloseStatus(err) != websocket.StatusInternalError {
		t.Fatalf("close status = %v (err=%v)", websocket.CloseStatus(err), err)
	}
	// The process and other connections are unaffected.
	write(t, bystander, sendEv("r2", conv1, "key-00000010", "still works"))
	if ev := readEnv(t, bystander); ev.Type != EvAccepted {
		t.Fatalf("bystander: %q", ev.Type)
	}
}

func TestQueueIsBoundedInBytesAsWellAsFrames(t *testing.T) {
	// The frame bound alone (1000) would allow 10 x 64 KiB; the byte bound (100 KiB) must not.
	e := newEnv(t, func(c *Config) { c.SendQueue = 1000; c.MaxQueueBytes = 100 << 10 })
	_ = e.dial(t, "tok-bob")
	body := strings.Repeat("x", 64<<10)
	for i := 0; i < 400; i++ {
		e.hub.Publish(messages.Event{
			Type: messages.EventCreated, Recipients: []string{"ub"},
			Message: domain.Message{ID: "big", ConversationID: conv1, Seq: int64(i + 1), ModSeq: int64(i + 1), Body: &body},
		})
	}
	waitFor(t, "byte-bounded queue to disconnect the slow client", func() bool { return e.hub.Stats().SlowConsumers >= 1 })
}

func TestSyncRequestsAreRateLimited(t *testing.T) {
	e := newEnv(t, nil)
	c := e.dial(t, "tok-alice")
	req := func(id string) map[string]any {
		return event(EvSyncRequest, id, map[string]any{"conversations": []map[string]any{{"conversation_id": conv1, "after_mod_seq": 0}}})
	}
	write(t, c, req("s1"))
	write(t, c, req("s2"))
	if ev := readEnv(t, c); ev.Type != EvSyncBatch || ev.RequestID != "s1" {
		t.Fatalf("first sync: %+v", ev)
	}
	if ev := readEnv(t, c); errCode(t, ev) != "rate_limited" || ev.RequestID != "s2" {
		t.Fatalf("second sync within a second must be limited: %+v", ev)
	}
	time.Sleep(1100 * time.Millisecond)
	write(t, c, req("s3"))
	if ev := readEnv(t, c); ev.Type != EvSyncBatch || ev.RequestID != "s3" {
		t.Fatalf("sync after the gap: %+v", ev)
	}
}
