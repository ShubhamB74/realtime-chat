//go:build integration

package httpapi_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func (a *api) direct(from person, to person) string {
	a.t.Helper()
	r := a.do("POST", "/v1/conversations/direct", from.Access, map[string]any{"user_id": to.ID})
	if r.Status != 200 && r.Status != 201 {
		a.t.Fatalf("open direct: %d %v", r.Status, r.Body)
	}
	return r.str("conversation", "id")
}

func (a *api) unreadOf(p person, conv string) int {
	a.t.Helper()
	l := a.expect(a.do("GET", "/v1/conversations", p.Access, nil), 200, "list")
	for _, it := range list(l, "conversations") {
		c := it.(map[string]any)
		if c["id"] == conv {
			return int(c["unread_count"].(float64))
		}
	}
	a.t.Fatalf("conversation %s not in the list", conv)
	return -1
}

// ---- two gateways, one database ----------------------------------------------------------------

func TestTwoGatewaysDeliverToEachOther(t *testing.T) {
	a := newAPI(t)
	gw2 := a.secondGateway("gw-2")
	alice, bob := a.signup("alice"), a.signup("bob")
	conv := a.direct(alice, bob)

	wa := a.wsDial(alice.Access)      // alice is on gateway 1
	wb := a.wsDialAt(gw2, bob.Access) // bob is on gateway 2

	wa.send("message.send", "r1", map[string]any{"conversation_id": conv, "idempotency_key": "gw-key-0000001", "body": "hello from gateway 1"})
	if acc := wa.next("message.accepted"); payload(acc)["message"].(map[string]any)["seq"].(float64) != 1 {
		t.Fatalf("accepted: %v", acc)
	}
	if got := payload(wb.next("message.received")); got["body"] != "hello from gateway 1" {
		t.Fatalf("bob (gateway 2) must receive alice's message through the bus: %v", got)
	}

	wb.send("message.send", "r2", map[string]any{"conversation_id": conv, "idempotency_key": "gw-key-0000002", "body": "reply from gateway 2"})
	wb.next("message.accepted")
	if got := payload(wa.next("message.received")); got["body"] != "reply from gateway 2" {
		t.Fatalf("alice (gateway 1) must receive bob's reply: %v", got)
	}

	// Stored once, in order.
	h := a.expect(a.do("GET", "/v1/conversations/"+conv+"/messages", alice.Access, nil), 200, "history")
	msgs := list(h, "messages")
	if len(msgs) != 2 || msgs[0].(map[string]any)["seq"].(float64) != 2 {
		t.Fatalf("history: %v", h.Body)
	}

	// REST edits on gateway 1 reach a socket on gateway 2.
	first := msgs[1].(map[string]any)["id"].(string)
	a.expect(a.do("PATCH", "/v1/conversations/"+conv+"/messages/"+first, alice.Access, map[string]any{"body": "hello (edited)"}), 200, "edit")
	if got := payload(wb.next("message.updated")); got["body"] != "hello (edited)" {
		t.Fatalf("live update across gateways: %v", got)
	}
}

func TestBusOutageDoesNotLoseMessages(t *testing.T) {
	a := newAPI(t)
	gw2 := a.secondGateway("gw-2")
	alice, bob := a.signup("alice"), a.signup("bob")
	conv := a.direct(alice, bob)
	wa, wb := a.wsDial(alice.Access), a.wsDialAt(gw2, bob.Access)

	a.mem.SetFailing(true) // registry and bus unavailable
	wa.send("message.send", "r1", map[string]any{"conversation_id": conv, "idempotency_key": "outage-key-0001", "body": "sent during the outage"})
	wa.next("message.accepted") // sending is unaffected: it only needs PostgreSQL
	a.mem.SetFailing(false)

	// Bob's gateway never got the push; the message is still there for his next sync.
	time.Sleep(1100 * time.Millisecond) // sync requests are limited to one per second per connection
	wb.send("sync.request", "s1", map[string]any{})
	b := payload(wb.next("sync.batch"))
	changes, _ := b["changes"].([]any)
	if b["conversation_id"] != conv || len(changes) != 1 || changes[0].(map[string]any)["body"] != "sent during the outage" {
		t.Fatalf("server-driven sync after the outage: %v", b)
	}
}

// ---- receipts, unread counts and server-driven sync ---------------------------------------------

func TestReceiptsAndUnreadCountsEndToEnd(t *testing.T) {
	a := newAPI(t)
	gw2 := a.secondGateway("gw-2")
	alice, bob, carol := a.signup("alice"), a.signup("bob"), a.signup("carol")
	conv := a.direct(alice, bob)
	for i := 1; i <= 3; i++ {
		a.expect(a.say(alice, conv, fmt.Sprintf("rcpt-key-%05d", i), fmt.Sprintf("m%d", i)), 201, "send")
	}
	if got := a.unreadOf(bob, conv); got != 3 {
		t.Fatalf("bob's unread = %d, want 3", got)
	}
	if got := a.unreadOf(alice, conv); got != 0 {
		t.Fatalf("alice's own messages are not unread for her: %d", got)
	}

	wa := a.wsDial(alice.Access)      // gateway 1
	wb := a.wsDialAt(gw2, bob.Access) // gateway 2

	// Bob's device acknowledges delivery: alice (other gateway) learns "delivered up to 3".
	wb.send("message.ack", "", map[string]any{"conversation_id": conv, "up_to_mod_seq": 3})
	r := payload(wa.next("receipt.updated"))
	if r["user_id"] != bob.ID || r["delivered_seq"].(float64) != 3 || r["read_seq"].(float64) != 0 {
		t.Fatalf("delivery receipt: %v", r)
	}

	// Bob reads up to 2 over the WebSocket (coalesced): alice sees it, unread drops to 1.
	wb.send("read.update", "", map[string]any{"conversation_id": conv, "up_to_seq": 2})
	r = payload(wa.next("receipt.updated"))
	if r["read_seq"].(float64) != 2 {
		t.Fatalf("read receipt: %v", r)
	}
	if got := a.unreadOf(bob, conv); got != 1 {
		t.Fatalf("after reading up to 2 bob's unread = %d, want 1", got)
	}

	// REST read mark, immediate; bob's own sockets are told too (other devices stay in sync).
	a.expect(a.do("POST", "/v1/conversations/"+conv+"/read", bob.Access, map[string]any{"up_to_seq": 3}), 204, "mark read")
	if got := a.unreadOf(bob, conv); got != 0 {
		t.Fatalf("unread after marking everything read = %d", got)
	}
	r = payload(wa.next("receipt.updated"))
	if r["read_seq"].(float64) != 3 {
		t.Fatalf("alice should see the new read position: %v", r)
	}
	// Cursors never move backwards, and are capped at the conversation counter.
	a.expect(a.do("POST", "/v1/conversations/"+conv+"/read", bob.Access, map[string]any{"up_to_seq": 1}), 204, "stale read mark")
	a.expect(a.do("POST", "/v1/conversations/"+conv+"/read", bob.Access, map[string]any{"up_to_seq": 99999}), 204, "huge read mark")
	rs := a.expect(a.do("GET", "/v1/conversations/"+conv+"/receipts", alice.Access, nil), 200, "receipts")
	for _, it := range list(rs, "receipts") {
		x := it.(map[string]any)
		if x["user_id"] == bob.ID && (x["read_seq"].(float64) != 3 || x["delivered_seq"].(float64) != 3) {
			t.Fatalf("bob's position: %v", x)
		}
	}

	// Strangers learn nothing and change nothing.
	a.expect(a.do("POST", "/v1/conversations/"+conv+"/read", carol.Access, map[string]any{"up_to_seq": 3}), 404, "stranger marks read")
	a.expect(a.do("GET", "/v1/conversations/"+conv+"/receipts", carol.Access, nil), 404, "stranger lists receipts")
	a.expect(a.do("POST", "/v1/conversations/"+conv+"/read", bob.Access, map[string]any{"up_to_seq": -1}), 400, "negative")
	a.expect(a.do("POST", "/v1/conversations/"+conv+"/read", bob.Access, map[string]any{}), 400, "missing up_to_seq")
	a.expect(a.do("POST", "/v1/conversations/nope/read", bob.Access, map[string]any{"up_to_seq": 1}), 404, "bad id")
}

func TestServerDrivenSyncAfterReconnect(t *testing.T) {
	a := newAPI(t)
	alice, bob := a.signup("alice"), a.signup("bob")
	conv := a.direct(alice, bob)
	for i := 1; i <= 3; i++ {
		a.expect(a.say(alice, conv, fmt.Sprintf("drv-key-%05d", i), fmt.Sprintf("while bob was away %d", i)), 201, "send")
	}

	wb := a.wsDial(bob.Access)
	wb.send("sync.request", "s1", nil) // no conversation list: the server finds what changed for this device
	b := payload(wb.next("sync.batch"))
	changes, _ := b["changes"].([]any)
	if b["conversation_id"] != conv || len(changes) != 3 || b["has_more"] != false || b["last_seq"].(float64) != 3 {
		t.Fatalf("first server-driven sync: %v", b)
	}

	// Acknowledge what was applied; the next sync (after a reconnect) has nothing left.
	wb.send("message.ack", "", map[string]any{"conversation_id": conv, "up_to_mod_seq": b["last_seq"]})
	time.Sleep(300 * time.Millisecond) // flush interval is 50 ms in tests
	_ = wb.c.Close(websocket.StatusNormalClosure, "")

	wb2 := a.wsDial(bob.Access)
	wb2.send("sync.request", "s2", nil)
	b2 := payload(wb2.next("sync.batch"))
	if c2, _ := b2["changes"].([]any); b2["conversation_id"] != "" || len(c2) != 0 || b2["done"] != true {
		t.Fatalf("nothing should be pending after the acknowledgement: %v", b2)
	}

	// New activity shows up again, from the acknowledged cursor.
	a.expect(a.say(alice, conv, "drv-key-00099", "one more"), 201, "send")
	time.Sleep(1100 * time.Millisecond)
	wb2.send("sync.request", "s3", nil)
	b3 := payload(wb2.next("sync.batch"))
	if c3, _ := b3["changes"].([]any); len(c3) != 1 || c3[0].(map[string]any)["body"] != "one more" {
		t.Fatalf("resumed sync: %v", b3)
	}
}

// ---- presence subscriptions ---------------------------------------------------------------------

func TestPresenceSubscriptionsEndToEnd(t *testing.T) {
	a := newAPI(t)
	gw2 := a.secondGateway("gw-2")
	alice, bob, carol := a.signup("alice"), a.signup("bob"), a.signup("carol")
	a.direct(alice, bob)

	wb := a.wsDial(bob.Access)
	wb.send("presence.subscribe", "p1", map[string]any{"user_ids": []string{alice.ID, carol.ID}})
	snap := payload(wb.next("presence.snapshot"))
	users, _ := snap["users"].([]any)
	if snap["available"] != true || len(users) != 1 || users[0].(map[string]any)["user_id"] != alice.ID || users[0].(map[string]any)["online"] != false {
		t.Fatalf("bob may watch alice (shared conversation) but not carol (none): %v", snap)
	}

	wa := a.wsDialAt(gw2, alice.Access) // alice comes online on the other gateway
	if got := payload(wb.next("presence.changed")); got["user_id"] != alice.ID || got["online"] != true {
		t.Fatalf("online event: %v", got)
	}
	_ = wa.c.Close(websocket.StatusNormalClosure, "")
	if got := payload(wb.next("presence.changed")); got["user_id"] != alice.ID || got["online"] != false {
		t.Fatalf("offline event after the grace period: %v", got)
	}

	// Carol shares no conversation with alice: her subscription is silently empty.
	wc := a.wsDial(carol.Access)
	wc.send("presence.subscribe", "p2", map[string]any{"user_ids": []string{alice.ID}})
	snap = payload(wc.next("presence.snapshot"))
	if u, _ := snap["users"].([]any); len(u) != 0 {
		t.Fatalf("carol must learn nothing about alice: %v", snap)
	}
}
