//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ---- helpers ---------------------------------------------------------------------------------

func (a *api) group(owner person, members ...person) string {
	a.t.Helper()
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ID)
	}
	r := a.expect(a.do("POST", "/v1/conversations/groups", owner.Access, map[string]any{"title": "Chat", "member_ids": ids}), 201, "create group")
	return r.str("conversation", "id")
}

func (a *api) say(p person, conv, key, body string) resp {
	a.t.Helper()
	return a.do("POST", "/v1/conversations/"+conv+"/messages", p.Access, map[string]any{"idempotency_key": key, "body": body})
}

func num(r resp, keys ...string) float64 {
	var cur any = r.Body
	for _, k := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			return -1
		}
		cur = m[k]
	}
	f, ok := cur.(float64)
	if !ok {
		return -1
	}
	return f
}

func list(r resp, key string) []any {
	l, _ := r.Body[key].([]any)
	return l
}

type wsc struct {
	a *api
	c *websocket.Conn
}

func (a *api) wsDial(token string) *wsc {
	a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/v1/ws"
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
	if err != nil {
		a.t.Fatalf("ws dial: %v", err)
	}
	a.t.Cleanup(func() { _ = c.CloseNow() })
	w := &wsc{a: a, c: c}
	w.next("connection.ready")
	return w
}

// next returns the next event of one of the wanted types, skipping any others.
func (w *wsc) next(types ...string) map[string]any {
	w.a.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		_, data, err := w.c.Read(ctx)
		if err != nil {
			w.a.t.Fatalf("waiting for %v: %v", types, err)
		}
		var ev map[string]any
		if err := json.Unmarshal(data, &ev); err != nil {
			w.a.t.Fatalf("bad frame: %v", err)
		}
		for _, want := range types {
			if ev["type"] == want {
				return ev
			}
		}
	}
}

func (w *wsc) send(typ, reqID string, payload any) {
	w.a.t.Helper()
	b, _ := json.Marshal(map[string]any{"v": 1, "type": typ, "request_id": reqID, "payload": payload})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := w.c.Write(ctx, websocket.MessageText, b); err != nil {
		w.a.t.Fatalf("ws write: %v", err)
	}
}

func payload(ev map[string]any) map[string]any {
	p, _ := ev["payload"].(map[string]any)
	return p
}

// ---- REST ------------------------------------------------------------------------------------

func TestMessagesRESTLifecycle(t *testing.T) {
	a := newAPI(t)
	alice, bob, carol := a.signup("alice"), a.signup("bob"), a.signup("carol")
	conv := a.group(alice, bob)
	dm := a.expect(a.do("POST", "/v1/conversations/direct", alice.Access, map[string]any{"user_id": bob.ID}), 201, "dm").str("conversation", "id")

	first := a.expect(a.say(alice, conv, "rest-key-0001", "hello"), 201, "send")
	if num(first, "message", "seq") != 1 || first.Body["duplicate"] != false {
		t.Fatalf("first send: %v", first.Body)
	}
	id1 := first.str("message", "id")
	again := a.expect(a.say(alice, conv, "rest-key-0001", "hello"), 200, "retry")
	if again.str("message", "id") != id1 || again.Body["duplicate"] != true {
		t.Fatalf("retry must return the original: %v", again.Body)
	}
	a.expect(a.say(alice, dm, "rest-key-0001", "hello"), 409, "same key, different conversation")
	for i := 2; i <= 4; i++ {
		a.expect(a.say(alice, conv, fmt.Sprintf("rest-key-000%d", i), fmt.Sprintf("m%d", i)), 201, "send more")
	}

	// History: newest first, paginated by seq.
	h := a.expect(a.do("GET", "/v1/conversations/"+conv+"/messages?limit=2", bob.Access, nil), 200, "history")
	if m := list(h, "messages"); len(m) != 2 || h.Body["has_more"] != true || m[0].(map[string]any)["seq"].(float64) != 4 {
		t.Fatalf("history page 1: %v", h.Body)
	}
	h2 := a.expect(a.do("GET", "/v1/conversations/"+conv+"/messages?before_seq=3&limit=10", bob.Access, nil), 200, "history page 2")
	if m := list(h2, "messages"); len(m) != 2 || h2.Body["has_more"] != false || m[1].(map[string]any)["seq"].(float64) != 1 {
		t.Fatalf("history page 2: %v", h2.Body)
	}

	// Sync: everything after a cursor, edits and deletions included.
	ch := a.expect(a.do("GET", "/v1/conversations/"+conv+"/changes?after_mod_seq=0", bob.Access, nil), 200, "changes")
	if len(list(ch, "changes")) != 4 || num(ch, "last_seq") != 4 {
		t.Fatalf("changes: %v", ch.Body)
	}
	path1 := "/v1/conversations/" + conv + "/messages/" + id1
	a.expect(a.do("PATCH", path1, bob.Access, map[string]any{"body": "hijack"}), 403, "edit someone else's message")
	ed := a.expect(a.do("PATCH", path1, alice.Access, map[string]any{"body": "hello (edited)"}), 200, "edit")
	if num(ed, "message", "seq") != 1 || num(ed, "message", "mod_seq") != 5 || ed.str("message", "edited_at") == "" {
		t.Fatalf("edit: %v", ed.Body)
	}
	ch2 := a.expect(a.do("GET", "/v1/conversations/"+conv+"/changes?after_mod_seq=4", bob.Access, nil), 200, "changes after edit")
	if c := list(ch2, "changes"); len(c) != 1 || c[0].(map[string]any)["body"] != "hello (edited)" {
		t.Fatalf("changes after edit: %v", ch2.Body)
	}
	a.expect(a.do("DELETE", path1, bob.Access, nil), 403, "member deletes another's message")
	del := a.expect(a.do("DELETE", path1, alice.Access, nil), 200, "delete")
	if del.Body["message"].(map[string]any)["body"] != nil || del.str("message", "deleted_at") == "" || num(del, "message", "mod_seq") != 6 {
		t.Fatalf("tombstone: %v", del.Body)
	}
	if again := a.expect(a.do("DELETE", path1, alice.Access, nil), 200, "delete again"); num(again, "message", "mod_seq") != 6 {
		t.Error("deleting twice must not bump mod_seq")
	}
	a.expect(a.do("PATCH", path1, alice.Access, map[string]any{"body": "zombie"}), 409, "edit a deleted message")

	// A stranger gets 404 everywhere, indistinguishable from a missing conversation.
	for name, r := range map[string]resp{
		"send":     a.say(carol, conv, "stranger-key-01", "let me in"),
		"history":  a.do("GET", "/v1/conversations/"+conv+"/messages", carol.Access, nil),
		"changes":  a.do("GET", "/v1/conversations/"+conv+"/changes", carol.Access, nil),
		"edit":     a.do("PATCH", path1, carol.Access, map[string]any{"body": "x"}),
		"delete":   a.do("DELETE", path1, carol.Access, nil),
		"presence": a.do("GET", "/v1/conversations/"+conv+"/presence", carol.Access, nil),
	} {
		if r.Status != 404 {
			t.Errorf("stranger %s: want 404, got %d (%v)", name, r.Status, r.Body)
		}
	}

	// Validation.
	a.expect(a.say(alice, conv, "valid-key-0001", ""), 400, "empty body")
	a.expect(a.say(alice, conv, "valid-key-0002", strings.Repeat("x", 4001)), 400, "body too long")
	a.expect(a.say(alice, conv, "short", "x"), 400, "short key")
	a.expect(a.do("GET", "/v1/conversations/"+conv+"/messages?before_seq=-1", alice.Access, nil), 400, "negative cursor")
	a.expect(a.do("GET", "/v1/conversations/"+conv+"/messages?limit=abc", alice.Access, nil), 400, "bad limit")
	a.expect(a.do("GET", "/v1/conversations/"+conv+"/changes?after_mod_seq=x", alice.Access, nil), 400, "bad cursor")
}

func TestNewMembersOnlySeeMessagesFromWhenTheyJoined(t *testing.T) {
	a := newAPI(t)
	alice, bob, carol := a.signup("alice"), a.signup("bob"), a.signup("carol")
	conv := a.group(alice, bob)
	a.expect(a.say(alice, conv, "early-key-0001", "secret from before"), 201, "early message")
	a.expect(a.do("POST", "/v1/conversations/"+conv+"/members", alice.Access, map[string]any{"user_ids": []string{carol.ID}}), 200, "add carol")

	h := a.expect(a.do("GET", "/v1/conversations/"+conv+"/messages", carol.Access, nil), 200, "carol history")
	if len(list(h, "messages")) != 0 {
		t.Fatalf("carol must not see messages from before she joined: %v", h.Body)
	}
	a.expect(a.say(alice, conv, "late-key-00001", "welcome carol"), 201, "late message")
	h = a.expect(a.do("GET", "/v1/conversations/"+conv+"/messages", carol.Access, nil), 200, "carol history 2")
	if m := list(h, "messages"); len(m) != 1 || m[0].(map[string]any)["body"] != "welcome carol" {
		t.Fatalf("carol history: %v", h.Body)
	}
	a.expect(a.do("DELETE", "/v1/conversations/"+conv+"/members/"+carol.ID, alice.Access, nil), 204, "remove carol")
	a.expect(a.do("GET", "/v1/conversations/"+conv+"/messages", carol.Access, nil), 404, "removed member has no access")
}

func TestConversationListShowsLastMessageAndCounter(t *testing.T) {
	a := newAPI(t)
	alice, bob := a.signup("alice"), a.signup("bob")
	conv := a.group(alice, bob)
	a.expect(a.say(alice, conv, "list-key-00001", "first"), 201, "send")
	last := a.expect(a.say(bob, conv, "list-key-00002", "latest"), 201, "send")

	l := a.expect(a.do("GET", "/v1/conversations", alice.Access, nil), 200, "list")
	items := list(l, "conversations")
	if len(items) != 1 {
		t.Fatalf("list: %v", l.Body)
	}
	c := items[0].(map[string]any)
	lm, _ := c["last_message"].(map[string]any)
	if lm == nil || lm["body"] != "latest" || lm["seq"].(float64) != 2 || lm["deleted"] != false || c["last_seq"].(float64) != 2 {
		t.Fatalf("conversation summary: %v", c)
	}
	a.expect(a.do("DELETE", "/v1/conversations/"+conv+"/messages/"+last.str("message", "id"), bob.Access, nil), 200, "delete latest")
	l = a.expect(a.do("GET", "/v1/conversations", alice.Access, nil), 200, "list after delete")
	lm, _ = list(l, "conversations")[0].(map[string]any)["last_message"].(map[string]any)
	if lm == nil || lm["deleted"] != true || lm["body"] != nil {
		t.Fatalf("a deleted last message is a tombstone: %v", lm)
	}
}

// ---- WebSocket, end to end ---------------------------------------------------------------------

func TestWebSocketEndToEnd(t *testing.T) {
	a := newAPI(t)
	alice, bob, carol := a.signup("alice"), a.signup("bob"), a.signup("carol")
	conv := a.group(alice, bob)
	wa, wb, wc := a.wsDial(alice.Access), a.wsDial(bob.Access), a.wsDial(carol.Access)

	// Send over WebSocket: durable acknowledgement to the sender, live delivery to the other member.
	wa.send("message.send", "r1", map[string]any{"conversation_id": conv, "idempotency_key": "ws-key-0000001", "body": "hi bob"})
	acc := wa.next("message.accepted")
	if acc["request_id"] != "r1" || payload(acc)["message"].(map[string]any)["seq"].(float64) != 1 {
		t.Fatalf("accepted: %v", acc)
	}
	got := wb.next("message.received")
	if payload(got)["body"] != "hi bob" || payload(got)["conversation_id"] != conv {
		t.Fatalf("bob received: %v", got)
	}
	// It is really stored: visible through REST history.
	h := a.expect(a.do("GET", "/v1/conversations/"+conv+"/messages", bob.Access, nil), 200, "history")
	if len(list(h, "messages")) != 1 {
		t.Fatalf("message was not persisted: %v", h.Body)
	}

	// A non-member cannot send, and the connection stays usable.
	wc.send("message.send", "r2", map[string]any{"conversation_id": conv, "idempotency_key": "ws-key-0000002", "body": "intruder"})
	if e := wc.next("error"); payload(e)["code"] != "not_found" || e["request_id"] != "r2" {
		t.Fatalf("intruder: %v", e)
	}

	// Reconnect + sync: bob goes offline, misses two messages, and catches up from his cursor.
	_ = wb.c.Close(websocket.StatusNormalClosure, "")
	wa.send("message.send", "r3", map[string]any{"conversation_id": conv, "idempotency_key": "ws-key-0000003", "body": "while you were away 1"})
	wa.next("message.accepted")
	wa.send("message.send", "r4", map[string]any{"conversation_id": conv, "idempotency_key": "ws-key-0000004", "body": "while you were away 2"})
	wa.next("message.accepted")

	wb2 := a.wsDial(bob.Access)
	wb2.send("sync.request", "sync1", map[string]any{"conversations": []map[string]any{{"conversation_id": conv, "after_mod_seq": 1}}})
	batch := payload(wb2.next("sync.batch"))
	changes, _ := batch["changes"].([]any)
	if batch["status"] != "ok" || len(changes) != 2 || batch["done"] != true || batch["last_seq"].(float64) != 3 {
		t.Fatalf("sync batch: %v", batch)
	}

	// Edits reach connected members live (REST has no origin connection, so Alice sees it too).
	first := list(a.expect(a.do("GET", "/v1/conversations/"+conv+"/messages?limit=1&before_seq=2", bob.Access, nil), 200, "find first"), "messages")[0].(map[string]any)
	a.expect(a.do("PATCH", "/v1/conversations/"+conv+"/messages/"+first["id"].(string), alice.Access, map[string]any{"body": "hi bob (edited)"}), 200, "edit")
	upd := wb2.next("message.updated")
	if payload(upd)["body"] != "hi bob (edited)" || payload(upd)["mod_seq"].(float64) != 4 {
		t.Fatalf("live update: %v", upd)
	}
	wa.next("message.updated")

	// Typing indicators.
	wa.send("typing.start", "", map[string]any{"conversation_id": conv})
	if ty := payload(wb2.next("typing.update")); ty["user_id"] != alice.ID || ty["typing"] != true {
		t.Fatalf("typing: %v", ty)
	}

	// Presence (memory-backed here; Redis in production).
	pr := a.expect(a.do("GET", "/v1/conversations/"+conv+"/presence", alice.Access, nil), 200, "presence")
	online, _ := pr.Body["online"].([]any)
	if pr.Body["available"] != true || len(online) != 2 {
		t.Fatalf("presence with alice and bob connected: %v", pr.Body)
	}

	// Oversized and invalid sends are refused without closing the socket.
	wa.send("message.send", "r5", map[string]any{"conversation_id": conv, "idempotency_key": "ws-key-0000005", "body": strings.Repeat("x", 5000)})
	if e := wa.next("error"); payload(e)["code"] != "invalid_request" {
		t.Fatalf("oversized body: %v", e)
	}
}

func TestRevokedSessionIsRefusedAndDisconnected(t *testing.T) {
	a := newAPI(t)
	bob := a.signup("bob")
	wb := a.wsDial(bob.Access)

	// Revoking the session closes the live socket (re-validated every 100 ms in this test).
	a.expect(a.do("DELETE", "/v1/sessions/"+bob.SessionID, bob.Access, nil), 204, "revoke own session")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := wb.c.Read(ctx)
	if websocket.CloseStatus(err) != 4001 {
		t.Fatalf("close status = %v (err=%v), want 4001", websocket.CloseStatus(err), err)
	}

	// The access token itself is still unexpired, but the handshake checks the session.
	url := "ws" + strings.TrimPrefix(a.srv.URL, "http") + "/v1/ws"
	_, resp, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + bob.Access}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("handshake with a revoked session: want 401, got err=%v resp=%v", err, resp)
	}
}
