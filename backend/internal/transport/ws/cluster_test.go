package ws

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"realtime-chat/backend/internal/app/messages"
	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/platform/gwbus"
)

// ---- harness: several gateways sharing one message store, one registry and one bus -------------

type fakeVisibility struct{ allowed map[string][]string } // viewer -> users they may watch

func (f fakeVisibility) VisibleUsers(_ context.Context, viewer string, candidates []string, _ int) ([]string, error) {
	ok := map[string]bool{}
	for _, u := range f.allowed[viewer] {
		ok[u] = true
	}
	var out []string
	for _, c := range candidates {
		if ok[c] {
			out = append(out, c)
		}
	}
	return out, nil
}

type applyCall struct {
	acks  []domain.DeviceAck
	reads []domain.ReadMark
}

type fakeReceipts struct {
	mu    sync.Mutex
	calls []applyCall
}

func (f *fakeReceipts) Apply(_ context.Context, a []domain.DeviceAck, r []domain.ReadMark) error {
	f.mu.Lock()
	f.calls = append(f.calls, applyCall{a, r})
	f.mu.Unlock()
	return nil
}

func (f *fakeReceipts) snapshot() []applyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]applyCall(nil), f.calls...)
}

type gateway struct {
	hub      *Hub
	srv      *httptest.Server
	msgs     *fakeMessages
	receipts *fakeReceipts
}

type cluster struct {
	t        *testing.T
	mem      *gwbus.Mem
	store    *fakeStore
	sessions *fakeSessions
	vis      fakeVisibility
	tokens   fakeTokens
}

func newCluster(t *testing.T) *cluster {
	t.Helper()
	return &cluster{
		t: t, mem: gwbus.NewMem(), store: newFakeStore(),
		sessions: &fakeSessions{inactive: map[string]bool{}},
		tokens: fakeTokens{
			"tok-alice": {UserID: "ua", SessionID: "sa", DeviceID: "da"},
			"tok-bob":   {UserID: "ub", SessionID: "sb", DeviceID: "db"},
			"tok-carol": {UserID: "uc", SessionID: "sc", DeviceID: "dc"},
		},
	}
}

func (c *cluster) addGateway(id string, mutate func(*Config)) *gateway {
	c.t.Helper()
	cfg := DefaultConfig()
	cfg.GatewayID = id
	cfg.PingInterval = time.Hour
	cfg.RevalidateInterval = time.Hour
	cfg.FlushInterval = 30 * time.Millisecond
	cfg.PresenceGrace = 100 * time.Millisecond
	if mutate != nil {
		mutate(&cfg)
	}
	hub := NewHub(cfg, Deps{
		Sessions: c.sessions, Presence: c.mem, Bus: c.mem, Visibility: c.vis, Tokens: c.tokens,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
	g := &gateway{hub: hub, msgs: &fakeMessages{hub: hub, fakeStore: c.store}, receipts: &fakeReceipts{}}
	hub.Bind(g.msgs)
	hub.BindReceipts(g.receipts)
	hub.Start()
	g.srv = httptest.NewServer(hub)
	c.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = hub.Shutdown(ctx)
		g.srv.Close()
	})
	return g
}

// waitReady blocks until the gateway's bus subscription is active.
func (c *cluster) waitReady(gs ...*gateway) {
	c.t.Helper()
	for _, g := range gs {
		waitFor(c.t, "bus subscription of "+g.hub.GatewayID(), func() bool { return c.mem.Subscribers(g.hub.GatewayID()) > 0 })
	}
}

func (g *gateway) dial(t *testing.T, token string) *websocket.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := "ws" + g.srv.URL[len("http"):]
	h := map[string][]string{"Authorization": {"Bearer " + token}}
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
	if err != nil {
		t.Fatalf("dial %s on %s: %v", token, g.hub.GatewayID(), err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	if ev := readEnv(t, c); ev.Type != EvReady {
		t.Fatalf("first event = %q", ev.Type)
	}
	return c
}

// ---- cross-gateway delivery -------------------------------------------------------------------

func TestMessageCrossesGateways(t *testing.T) {
	cl := newCluster(t)
	gw1, gw2 := cl.addGateway("gw1", nil), cl.addGateway("gw2", nil)
	cl.waitReady(gw1, gw2)

	alice := gw1.dial(t, "tok-alice")
	bobHere, bobThere := gw1.dial(t, "tok-bob"), gw2.dial(t, "tok-bob") // bob has a device on each gateway
	carol := gw2.dial(t, "tok-carol")                                    // not a member

	write(t, alice, sendEv("r1", conv1, "xgw-key-00001", "hello across gateways"))
	if ev := readEnv(t, alice); ev.Type != EvAccepted {
		t.Fatalf("alice: %q", ev.Type)
	}
	for name, c := range map[string]*websocket.Conn{"bob on gw1 (local)": bobHere, "bob on gw2 (via bus)": bobThere} {
		ev := readEnv(t, c)
		var m struct {
			Body *string `json:"body"`
		}
		payloadOf(t, ev, &m)
		if ev.Type != EvReceived || m.Body == nil || *m.Body != "hello across gateways" {
			t.Fatalf("%s: %+v", name, ev)
		}
	}
	waitFor(t, "remote counters", func() bool {
		return gw1.hub.Stats().RemotePublished >= 1 && gw2.hub.Stats().RemoteReceived >= 1
	})
	expectSilence(t, bobHere, 200*time.Millisecond)  // exactly one copy per connection
	expectSilence(t, bobThere, 200*time.Millisecond) // no duplicate from both paths
	expectSilence(t, carol, 200*time.Millisecond)    // a non-member on the target gateway gets nothing
}

func TestUpdatesAndTypingCrossGateways(t *testing.T) {
	cl := newCluster(t)
	gw1, gw2 := cl.addGateway("gw1", nil), cl.addGateway("gw2", nil)
	cl.waitReady(gw1, gw2)
	alice, bob := gw1.dial(t, "tok-alice"), gw2.dial(t, "tok-bob")

	body := "edited"
	gw1.hub.Publish(messages.Event{Type: messages.EventUpdated, Recipients: []string{"ua", "ub"},
		Message: domain.Message{ID: "m1", ConversationID: conv1, Seq: 1, ModSeq: 4, Body: &body}})
	if ev := readEnv(t, bob); ev.Type != EvUpdated {
		t.Fatalf("bob: %q", ev.Type)
	}
	if ev := readEnv(t, alice); ev.Type != EvUpdated {
		t.Fatalf("alice (same gateway): %q", ev.Type)
	}

	write(t, alice, event(EvTypingStart, "", map[string]any{"conversation_id": conv1}))
	if ev := readEnv(t, bob); ev.Type != EvTyping {
		t.Fatalf("typing did not cross gateways: %q", ev.Type)
	}
}

func TestRegistryOrBusOutageFallsBackToSyncWithoutLoss(t *testing.T) {
	cl := newCluster(t)
	gw1, gw2 := cl.addGateway("gw1", nil), cl.addGateway("gw2", nil)
	cl.waitReady(gw1, gw2)
	alice, bobSame, bobOther := gw1.dial(t, "tok-alice"), gw1.dial(t, "tok-bob"), gw2.dial(t, "tok-bob")

	cl.mem.SetFailing(true) // the registry and the bus go down
	write(t, alice, sendEv("r1", conv1, "outage-key-001", "sent during the outage"))
	if ev := readEnv(t, alice); ev.Type != EvAccepted {
		t.Fatalf("sending must keep working: %q", ev.Type)
	}
	if ev := readEnv(t, bobSame); ev.Type != EvReceived {
		t.Fatalf("local delivery must keep working: %q", ev.Type)
	}
	waitFor(t, "the failure to be counted", func() bool { return gw1.hub.Stats().RemoteFailed >= 1 })

	cl.mem.SetFailing(false)
	// Bob's other device missed the push, but nothing is lost: it syncs from its cursor.
	write(t, bobOther, event(EvSyncRequest, "s1", map[string]any{"conversations": []map[string]any{{"conversation_id": conv1, "after_mod_seq": 0}}}))
	ev := readEnv(t, bobOther)
	var b syncBatch
	payloadOf(t, ev, &b)
	if ev.Type != EvSyncBatch || b.Status != "ok" || len(b.Changes) != 1 || b.Changes[0].Body == nil || *b.Changes[0].Body != "sent during the outage" {
		t.Fatalf("sync after the outage: %+v / %+v", ev, b)
	}
}

func TestBusSubscriptionRecoversAfterAnOutage(t *testing.T) {
	cl := newCluster(t)
	cl.mem.SetFailing(true)
	gw1, gw2 := cl.addGateway("gw1", nil), cl.addGateway("gw2", nil) // subscriptions fail and retry with backoff
	time.Sleep(300 * time.Millisecond)
	cl.mem.SetFailing(false)
	cl.waitReady(gw1, gw2)

	alice, bob := gw1.dial(t, "tok-alice"), gw2.dial(t, "tok-bob")
	write(t, alice, sendEv("r1", conv1, "recover-key-01", "after recovery"))
	readEnv(t, alice)
	if ev := readEnv(t, bob); ev.Type != EvReceived {
		t.Fatalf("cross-gateway delivery must resume once the bus is back: %q", ev.Type)
	}
}

// ---- presence subscriptions -------------------------------------------------------------------

func TestPresenceSubscriptionsFollowUsersAcrossGateways(t *testing.T) {
	cl := newCluster(t)
	cl.vis = fakeVisibility{allowed: map[string][]string{"ub": {"ua"}}} // bob may watch alice, not carol
	gw1, gw2 := cl.addGateway("gw1", nil), cl.addGateway("gw2", nil)
	cl.waitReady(gw1, gw2)
	bob := gw2.dial(t, "tok-bob")

	write(t, bob, event(EvPresenceSubscribe, "p1", map[string]any{"user_ids": []string{"ua", "uc"}}))
	ev := readEnv(t, bob)
	var snap presenceSnapshot
	payloadOf(t, ev, &snap)
	if ev.Type != EvPresenceSnapshot || !snap.Available || len(snap.Users) != 1 || snap.Users[0].UserID != "ua" || snap.Users[0].Online {
		t.Fatalf("snapshot must list only visible users, offline: %+v", snap)
	}

	alice := gw1.dial(t, "tok-alice") // alice comes online on the OTHER gateway
	ev = readEnv(t, bob)
	var ch presenceChanged
	payloadOf(t, ev, &ch)
	if ev.Type != EvPresenceChanged || ch.UserID != "ua" || !ch.Online {
		t.Fatalf("online event: %+v / %+v", ev, ch)
	}

	_ = alice.Close(websocket.StatusNormalClosure, "")
	ev = readEnv(t, bob) // after the grace period
	payloadOf(t, ev, &ch)
	if ev.Type != EvPresenceChanged || ch.UserID != "ua" || ch.Online {
		t.Fatalf("offline event: %+v / %+v", ev, ch)
	}

	write(t, bob, event(EvPresenceUnsubscribe, "", map[string]any{"user_ids": []string{"ua"}}))
	time.Sleep(50 * time.Millisecond)
	_ = gw1.dial(t, "tok-alice")
	expectSilence(t, bob, 300*time.Millisecond)
}

func TestPresenceReconnectWithinGraceIsNotAnnouncedOffline(t *testing.T) {
	cl := newCluster(t)
	cl.vis = fakeVisibility{allowed: map[string][]string{"ub": {"ua"}}}
	gw1, gw2 := cl.addGateway("gw1", func(c *Config) { c.PresenceGrace = 400 * time.Millisecond }), cl.addGateway("gw2", nil)
	cl.waitReady(gw1, gw2)
	bob := gw2.dial(t, "tok-bob")
	write(t, bob, event(EvPresenceSubscribe, "p1", map[string]any{"user_ids": []string{"ua"}}))
	readEnv(t, bob)

	first := gw1.dial(t, "tok-alice")
	if ev := readEnv(t, bob); ev.Type != EvPresenceChanged {
		t.Fatalf("online: %q", ev.Type)
	}
	_ = first.Close(websocket.StatusNormalClosure, "")
	time.Sleep(100 * time.Millisecond)
	_ = gw1.dial(t, "tok-alice") // reconnects inside the grace period
	for i := 0; i < 2; i++ {     // a repeated "online" is allowed; an "offline" is not
		ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
		_, data, err := bob.Read(ctx)
		cancel()
		if err != nil {
			break
		}
		var ev Envelope
		_ = jsonUnmarshal(data, &ev)
		var ch presenceChanged
		payloadOf(t, ev, &ch)
		if !ch.Online {
			t.Fatal("a quick reconnect must not be announced as offline")
		}
	}
}

func TestPresenceSubscriptionCapAndValidation(t *testing.T) {
	cl := newCluster(t)
	cl.vis = fakeVisibility{allowed: map[string][]string{"ub": {"ua", "uc"}}}
	gw := cl.addGateway("gw1", func(c *Config) { c.MaxPresenceSubs = 1 })
	cl.waitReady(gw)
	bob := gw.dial(t, "tok-bob")
	write(t, bob, event(EvPresenceSubscribe, "p1", map[string]any{"user_ids": []string{"ua", "uc"}}))
	if got := errCode(t, readEnv(t, bob)); got != "invalid_request" {
		t.Errorf("over the cap: %s", got)
	}
	write(t, bob, event(EvPresenceSubscribe, "p2", map[string]any{"user_ids": []string{"not-a-uuid"}}))
	if got := errCode(t, readEnv(t, bob)); got != "invalid_request" {
		t.Errorf("malformed id: %s", got)
	}
	write(t, bob, event(EvPresenceSubscribe, "p3", map[string]any{"user_ids": []string{}}))
	if got := errCode(t, readEnv(t, bob)); got != "invalid_request" {
		t.Errorf("empty list: %s", got)
	}
}

// ---- receipts: coalescing and publishing ---------------------------------------------------------

func TestAcksAndReadMarksAreCoalescedAndAttributedToTheCaller(t *testing.T) {
	cl := newCluster(t)
	gw := cl.addGateway("gw1", nil)
	cl.waitReady(gw)
	alice := gw.dial(t, "tok-alice")

	for _, n := range []int{1, 2, 3} {
		write(t, alice, event(EvMessageAck, "", map[string]any{"conversation_id": conv1, "up_to_mod_seq": n}))
	}
	for _, n := range []int{2, 5} {
		write(t, alice, event(EvReadUpdate, "", map[string]any{"conversation_id": conv1, "up_to_seq": n}))
	}
	waitFor(t, "cursors to be flushed", func() bool {
		var acked, read int64
		for _, c := range gw.receipts.snapshot() {
			for _, a := range c.acks {
				acked = max(acked, a.UpToModSeq)
			}
			for _, r := range c.reads {
				read = max(read, r.UpToSeq)
			}
		}
		return acked == 3 && read == 5
	})
	for _, c := range gw.receipts.snapshot() {
		for _, a := range c.acks {
			if a.UserID != "ua" || a.DeviceID != "da" || a.ConversationID != conv1 {
				t.Fatalf("ack must carry the authenticated identity: %+v", a)
			}
		}
	}
	if n := len(gw.receipts.snapshot()); n > 3 {
		t.Errorf("5 events should coalesce into very few writes, got %d Apply calls", n)
	}

	write(t, alice, event(EvMessageAck, "bad1", map[string]any{"conversation_id": "nope", "up_to_mod_seq": 1}))
	if got := errCode(t, readEnv(t, alice)); got != "invalid_request" {
		t.Errorf("bad conversation id: %s", got)
	}
	write(t, alice, event(EvReadUpdate, "bad2", map[string]any{"conversation_id": conv1, "up_to_seq": -1}))
	if got := errCode(t, readEnv(t, alice)); got != "invalid_request" {
		t.Errorf("negative cursor: %s", got)
	}
}

func TestShutdownFlushesBufferedCursors(t *testing.T) {
	cl := newCluster(t)
	gw := cl.addGateway("gw1", func(c *Config) { c.FlushInterval = time.Hour })
	cl.waitReady(gw)
	alice := gw.dial(t, "tok-alice")
	write(t, alice, event(EvMessageAck, "", map[string]any{"conversation_id": conv1, "up_to_mod_seq": 7}))
	time.Sleep(100 * time.Millisecond) // let the read goroutine buffer it

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := gw.hub.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	var got int64
	for _, c := range gw.receipts.snapshot() {
		for _, a := range c.acks {
			got = max(got, a.UpToModSeq)
		}
	}
	if got != 7 {
		t.Fatalf("graceful shutdown must write buffered cursors, got %d", got)
	}
}

func TestPublishReceiptRoutingAndDeduplication(t *testing.T) {
	cl := newCluster(t)
	gw1, gw2 := cl.addGateway("gw1", nil), cl.addGateway("gw2", nil)
	cl.waitReady(gw1, gw2)
	alice, bob := gw2.dial(t, "tok-alice"), gw1.dial(t, "tok-bob")

	direct := domain.ReceiptState{ConversationID: conv1, UserID: "ub", Delivered: 3, Read: 2, Direct: true, Others: []string{"ua"}}
	gw1.hub.PublishReceipt(direct)
	for name, c := range map[string]*websocket.Conn{"the other participant (other gateway)": alice, "bob's own devices": bob} {
		ev := readEnv(t, c)
		var r receiptUpdate
		payloadOf(t, ev, &r)
		if ev.Type != EvReceipt || r.UserID != "ub" || r.DeliveredSeq != 3 || r.ReadSeq != 2 {
			t.Fatalf("%s: %+v / %+v", name, ev, r)
		}
	}

	gw1.hub.PublishReceipt(direct) // unchanged: not pushed again
	direct.Delivered = 4           // only delivery moved: the other participant hears about it, bob does not
	gw1.hub.PublishReceipt(direct)
	ev := readEnv(t, alice)
	var r receiptUpdate
	payloadOf(t, ev, &r)
	if ev.Type != EvReceipt || r.DeliveredSeq != 4 {
		t.Fatalf("alice should get exactly the new delivery state: %+v", r)
	}
	expectSilence(t, alice, 200*time.Millisecond)
	expectSilence(t, bob, 200*time.Millisecond)
}

func TestGroupReceiptsOnlyReachTheMembersOwnDevices(t *testing.T) {
	cl := newCluster(t)
	gw := cl.addGateway("gw1", nil)
	cl.waitReady(gw)
	alice, bob := gw.dial(t, "tok-alice"), gw.dial(t, "tok-bob")

	group := domain.ReceiptState{ConversationID: conv1, UserID: "ub", Delivered: 5, Read: 0, Direct: false}
	gw.hub.PublishReceipt(group) // first sight counts as a read change: bob's devices are told
	if ev := readEnv(t, bob); ev.Type != EvReceipt {
		t.Fatalf("bob: %q", ev.Type)
	}
	group.Delivered = 6 // delivery progress in a group is not pushed (seen-by is on demand over REST)
	gw.hub.PublishReceipt(group)
	group.Read = 6 // read progress syncs bob's other devices, but is not pushed to the group
	gw.hub.PublishReceipt(group)
	if ev := readEnv(t, bob); ev.Type != EvReceipt {
		t.Fatalf("bob after read progress: %q", ev.Type)
	}
	expectSilence(t, alice, 250*time.Millisecond)
}

// ---- server-driven sync -----------------------------------------------------------------------

func TestServerDrivenSyncUsesTheDevicesCursors(t *testing.T) {
	cl := newCluster(t)
	gw := cl.addGateway("gw1", nil)
	cl.waitReady(gw)
	alice := gw.dial(t, "tok-alice")
	for i, key := range []string{"drv-key-000001", "drv-key-000002", "drv-key-000003"} {
		write(t, alice, sendEv("s"+string(rune('0'+i)), conv1, key, "m"))
		readEnv(t, alice)
	}

	// Nothing pending: one empty "done" batch.
	write(t, alice, map[string]any{"v": 1, "type": EvSyncRequest, "request_id": "d0"})
	var b syncBatch
	ev := readEnv(t, alice)
	payloadOf(t, ev, &b)
	if ev.Type != EvSyncBatch || b.Status != "ok" || b.ConversationID != "" || !b.Done || len(b.Changes) != 0 {
		t.Fatalf("nothing pending: %+v", b)
	}

	cl.store.mu.Lock()
	cl.store.pending = []domain.PendingSync{{ConversationID: conv1, Cursor: 1}}
	cl.store.mu.Unlock()
	time.Sleep(1100 * time.Millisecond) // sync requests are limited to one per second
	write(t, alice, event(EvSyncRequest, "d1", map[string]any{})) // an empty payload object is server-driven too
	ev = readEnv(t, alice)
	b = syncBatch{}
	payloadOf(t, ev, &b)
	if ev.RequestID != "d1" || b.ConversationID != conv1 || len(b.Changes) != 2 || b.Changes[0].ModSeq != 2 || !b.Done {
		t.Fatalf("server-driven batch: %+v", b)
	}
}

func TestGatewayIDs(t *testing.T) {
	h := NewHub(Config{GatewayID: "a/b"}, Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if h.GatewayID() != "a_b" {
		t.Errorf("slashes must be replaced: %q", h.GatewayID())
	}
	g1 := NewHub(Config{}, Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	g2 := NewHub(Config{}, Deps{Logger: slog.New(slog.NewJSONHandler(io.Discard, nil))})
	if g1.GatewayID() == g2.GatewayID() || len(g1.GatewayID()) < 5 {
		t.Errorf("generated ids must be distinct: %q %q", g1.GatewayID(), g2.GatewayID())
	}
	var _ auth.Identity // keep the auth import used by the harness types
}

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }
