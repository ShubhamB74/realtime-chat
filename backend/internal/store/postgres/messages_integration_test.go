//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/store/postgres"
)

type actor struct {
	user domain.User
	dev  domain.Device
}

func mkActor(t *testing.T, s *postgres.Store) actor {
	t.Helper()
	u := mkUser(t, s)
	d, _, _ := login(t, s, u.ID, 10)
	return actor{user: u, dev: d}
}

func send(t *testing.T, s *postgres.Store, conv string, who actor, key, body string) domain.MessageResult {
	t.Helper()
	res, err := s.SendMessage(context.Background(), domain.SendParams{
		ConversationID: conv, UserID: who.user.ID, DeviceID: who.dev.ID, IdempotencyKey: key, Body: body,
	})
	if err != nil {
		t.Fatalf("send %q: %v", key, err)
	}
	return res
}

func mkGroupOf(t *testing.T, s *postgres.Store, owner actor, others ...actor) string {
	t.Helper()
	us := make([]domain.User, 0, len(others))
	for _, o := range others {
		us = append(us, o.user)
	}
	return mkGroup(t, s, owner.user, us...).ID
}

func TestSendAssignsGaplessSequenceUnderConcurrency(t *testing.T) {
	s := store(t)
	a, b := mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)

	const n = 40
	seqs := make([]int64, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			who := a
			if i%2 == 1 {
				who = b
			}
			res, err := s.SendMessage(context.Background(), domain.SendParams{
				ConversationID: conv, UserID: who.user.ID, DeviceID: who.dev.ID,
				IdempotencyKey: "concurrent-key-" + string(rune('a'+i%26)) + string(rune('A'+i/26)), Body: "hi",
			})
			if err != nil {
				t.Errorf("send %d: %v", i, err)
				return
			}
			seqs[i] = res.Message.Seq
		}(i)
	}
	wg.Wait()
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	for i, sq := range seqs {
		if sq != int64(i+1) {
			t.Fatalf("sequence must be exactly 1..%d without gaps or duplicates, got %v", n, seqs)
		}
	}
	c, _ := s.GetConversation(context.Background(), conv)
	if c.LastSeq != n {
		t.Errorf("counter = %d, want %d", c.LastSeq, n)
	}
}

func TestSendIsIdempotent(t *testing.T) {
	s := store(t)
	a, b := mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)
	other := mkGroupOf(t, s, a, b)

	first := send(t, s, conv, a, "idem-key-0001", "once")
	again := send(t, s, conv, a, "idem-key-0001", "once")
	if first.Duplicate || !again.Duplicate || again.Message.ID != first.Message.ID || again.Message.Seq != first.Message.Seq {
		t.Fatalf("first=%+v again=%+v", first, again)
	}
	if c, _ := s.GetConversation(context.Background(), conv); c.LastSeq != 1 {
		t.Errorf("a retry must not consume a sequence number: counter = %d", c.LastSeq)
	}

	// Same key from the same device for a different conversation is a client bug: conflict.
	_, err := s.SendMessage(context.Background(), domain.SendParams{
		ConversationID: other, UserID: a.user.ID, DeviceID: a.dev.ID, IdempotencyKey: "idem-key-0001", Body: "once"})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("key reuse across conversations: want conflict, got %v", err)
	}

	// Eight racing retries of one new key store exactly one message.
	var wg sync.WaitGroup
	results := make([]domain.MessageResult, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := s.SendMessage(context.Background(), domain.SendParams{
				ConversationID: conv, UserID: a.user.ID, DeviceID: a.dev.ID, IdempotencyKey: "idem-race-0001", Body: "race"})
			if err != nil {
				t.Errorf("race %d: %v", i, err)
				return
			}
			results[i] = res
		}(i)
	}
	wg.Wait()
	fresh := 0
	for _, r := range results {
		if !r.Duplicate {
			fresh++
		}
		if r.Message.ID != results[0].Message.ID {
			t.Fatal("all retries must return the same message")
		}
	}
	if fresh != 1 {
		t.Fatalf("exactly one of the racing sends may create the message, got %d", fresh)
	}
}

func TestOnlyActiveMembersCanWrite(t *testing.T) {
	s := store(t)
	a, b, stranger := mkActor(t, s), mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)
	ctx := context.Background()

	_, err := s.SendMessage(ctx, domain.SendParams{ConversationID: conv, UserID: stranger.user.ID, DeviceID: stranger.dev.ID, IdempotencyKey: "stranger-key-1", Body: "hi"})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stranger: want not found, got %v", err)
	}
	m := send(t, s, conv, a, "member-key-0001", "hello")
	if err := s.RemoveMember(ctx, conv, b.user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendMessage(ctx, domain.SendParams{ConversationID: conv, UserID: b.user.ID, DeviceID: b.dev.ID, IdempotencyKey: "removed-key-001", Body: "hi"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("removed member send: want not found, got %v", err)
	}
	if _, err := s.EditMessage(ctx, conv, m.Message.ID, b.user.ID, "x"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("removed member edit: want not found, got %v", err)
	}
	if _, err := s.DeleteMessage(ctx, conv, m.Message.ID, b.user.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("removed member delete: want not found, got %v", err)
	}
	if _, _, err := s.ListHistory(ctx, conv, b.user.ID, 0, 10); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("removed member history: want not found, got %v", err)
	}
	if _, err := s.ListChanges(ctx, conv, b.user.ID, 0, 10); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("removed member changes: want not found, got %v", err)
	}
}

func TestEditAndDeleteRules(t *testing.T) {
	s := store(t)
	admin, bob := mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, admin, bob)
	ctx := context.Background()

	byBob := send(t, s, conv, bob, "bob-msg-000001", "from bob").Message
	byAdmin := send(t, s, conv, admin, "adm-msg-000001", "from admin").Message

	if _, err := s.EditMessage(ctx, conv, byAdmin.ID, bob.user.ID, "hijack"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("editing someone else's message: want forbidden, got %v", err)
	}
	if _, err := s.EditMessage(ctx, conv, byBob.ID, admin.user.ID, "admin edit"); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("admins may delete but not edit others' messages: got %v", err)
	}
	edited, err := s.EditMessage(ctx, conv, byBob.ID, bob.user.ID, "bob edited")
	if err != nil {
		t.Fatal(err)
	}
	m := edited.Message
	if m.Seq != byBob.Seq || m.ModSeq != 3 || m.EditedAt == nil || m.Body == nil || *m.Body != "bob edited" {
		t.Fatalf("edit must keep seq, bump mod_seq (3) and set edited_at: %+v", m)
	}
	if len(edited.Recipients) != 2 {
		t.Errorf("recipients = %v", edited.Recipients)
	}

	if _, err := s.DeleteMessage(ctx, conv, byAdmin.ID, bob.user.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a member may not delete another's message: got %v", err)
	}
	del, err := s.DeleteMessage(ctx, conv, byBob.ID, admin.user.ID) // admin may delete anyone's
	if err != nil {
		t.Fatal(err)
	}
	if del.Duplicate || del.Message.Body != nil || del.Message.DeletedAt == nil || del.Message.ModSeq != 4 {
		t.Fatalf("tombstone: %+v", del.Message)
	}
	again, err := s.DeleteMessage(ctx, conv, byBob.ID, bob.user.ID)
	if err != nil || !again.Duplicate || again.Message.ModSeq != 4 {
		t.Fatalf("second delete must be a no-op: %+v err=%v", again, err)
	}
	if _, err := s.EditMessage(ctx, conv, byBob.ID, bob.user.ID, "zombie"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("editing a deleted message: want conflict, got %v", err)
	}
	if c, _ := s.GetConversation(ctx, conv); c.LastSeq != 4 {
		t.Errorf("counter = %d, want 4 (2 sends, 1 edit, 1 delete; the no-op consumed nothing)", c.LastSeq)
	}
}

func TestChangesAreOrderedByModSeqAndHistoryByDisplayOrder(t *testing.T) {
	s := store(t)
	a, b := mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)
	ctx := context.Background()

	m1 := send(t, s, conv, a, "order-key-0001", "one").Message
	m2 := send(t, s, conv, a, "order-key-0002", "two").Message
	m3 := send(t, s, conv, a, "order-key-0003", "three").Message
	if _, err := s.EditMessage(ctx, conv, m1.ID, a.user.ID, "one edited"); err != nil { // mod_seq 4
		t.Fatal(err)
	}
	if _, err := s.DeleteMessage(ctx, conv, m2.ID, a.user.ID); err != nil { // mod_seq 5
		t.Fatal(err)
	}

	all, err := s.ListChanges(ctx, conv, b.user.ID, 0, 10)
	if err != nil || len(all.Messages) != 3 || all.LastSeq != 5 || all.HasMore {
		t.Fatalf("changes: %+v err=%v", all, err)
	}
	if all.Messages[0].ID != m3.ID || all.Messages[1].ID != m1.ID || all.Messages[2].ID != m2.ID {
		t.Fatalf("changes must be ordered by mod_seq (m3, m1, m2), got seqs %d,%d,%d", all.Messages[0].Seq, all.Messages[1].Seq, all.Messages[2].Seq)
	}
	after3, _ := s.ListChanges(ctx, conv, b.user.ID, 3, 10)
	if len(after3.Messages) != 2 || after3.Messages[0].ID != m1.ID {
		t.Fatalf("changes after cursor 3: %+v", after3)
	}
	page, _ := s.ListChanges(ctx, conv, b.user.ID, 0, 2)
	if len(page.Messages) != 2 || !page.HasMore {
		t.Fatalf("paged changes: %+v", page)
	}

	hist, more, err := s.ListHistory(ctx, conv, b.user.ID, 0, 10)
	if err != nil || more || len(hist) != 3 || hist[0].Seq != 3 || hist[1].Seq != 2 || hist[2].Seq != 1 {
		t.Fatalf("history must be newest first by seq: %+v more=%v err=%v", hist, more, err)
	}
	if hist[1].Body != nil || hist[1].DeletedAt == nil {
		t.Error("history includes the tombstone with a null body")
	}
	older, more, _ := s.ListHistory(ctx, conv, b.user.ID, 3, 1)
	if len(older) != 1 || older[0].Seq != 2 || !more {
		t.Fatalf("before_seq paging: %+v more=%v", older, more)
	}
}

func TestHistoryStartsAtTheMembersJoinPoint(t *testing.T) {
	s := store(t)
	a, b, c := mkActor(t, s), mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)
	ctx := context.Background()

	m1 := send(t, s, conv, a, "join-key-00001", "before carol 1").Message
	send(t, s, conv, a, "join-key-00002", "before carol 2")
	if _, err := s.AddMembers(ctx, conv, []string{c.user.ID}, 10); err != nil {
		t.Fatal(err)
	}
	if h, _, _ := s.ListHistory(ctx, conv, c.user.ID, 0, 10); len(h) != 0 {
		t.Fatalf("a new member sees nothing from before joining, got %d messages", len(h))
	}
	m3 := send(t, s, conv, a, "join-key-00003", "after carol joined").Message
	h, _, _ := s.ListHistory(ctx, conv, c.user.ID, 0, 10)
	if len(h) != 1 || h[0].ID != m3.ID {
		t.Fatalf("carol history: %+v", h)
	}
	ch, _ := s.ListChanges(ctx, conv, c.user.ID, 0, 10)
	if len(ch.Messages) != 1 || ch.Messages[0].ID != m3.ID {
		t.Fatalf("carol changes: %+v", ch)
	}
	// Edits of old messages must not leak to someone who joined later, and old messages are untouchable.
	if _, err := s.EditMessage(ctx, conv, m1.ID, a.user.ID, "edited later"); err != nil {
		t.Fatal(err)
	}
	if ch, _ := s.ListChanges(ctx, conv, c.user.ID, 0, 10); len(ch.Messages) != 1 {
		t.Errorf("an edit of a pre-join message must stay invisible to carol, got %d changes", len(ch.Messages))
	}
	if _, err := s.DeleteMessage(ctx, conv, m1.ID, c.user.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("pre-join message must look nonexistent to carol: %v", err)
	}

	// Removed and re-added: the gap while she was away stays hidden.
	if err := s.RemoveMember(ctx, conv, c.user.ID); err != nil {
		t.Fatal(err)
	}
	send(t, s, conv, a, "join-key-00004", "while carol was away")
	if _, err := s.AddMembers(ctx, conv, []string{c.user.ID}, 10); err != nil {
		t.Fatal(err)
	}
	if h, _, _ := s.ListHistory(ctx, conv, c.user.ID, 0, 10); len(h) != 0 {
		t.Fatalf("after re-adding, history starts fresh; got %d messages", len(h))
	}
}

func TestListForUserIncludesLastMessagePreview(t *testing.T) {
	s := store(t)
	a, b := mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)
	ctx := context.Background()

	if l, _, _ := s.ListForUser(ctx, a.user.ID, nil, 10); len(l) != 1 || l[0].LastMessage != nil {
		t.Fatalf("no messages yet: %+v", l)
	}
	send(t, s, conv, a, "prev-key-00001", "short")
	long := send(t, s, conv, b, "prev-key-00002", strings.Repeat("é", 300)).Message
	l, _, _ := s.ListForUser(ctx, a.user.ID, nil, 10)
	lm := l[0].LastMessage
	if lm == nil || lm.Seq != 2 || lm.Deleted || lm.Body == nil || len([]rune(*lm.Body)) != 141 || !strings.HasSuffix(*lm.Body, "…") {
		t.Fatalf("preview must be the newest message truncated to 140 runes + ellipsis: %+v", lm)
	}
	if _, err := s.DeleteMessage(ctx, conv, long.ID, b.user.ID); err != nil {
		t.Fatal(err)
	}
	l, _, _ = s.ListForUser(ctx, a.user.ID, nil, 10)
	if lm := l[0].LastMessage; lm == nil || !lm.Deleted || lm.Body != nil {
		t.Fatalf("a deleted last message shows as a tombstone: %+v", lm)
	}
	if l[0].LastSeq != 3 {
		t.Errorf("last_seq = %d, want 3", l[0].LastSeq)
	}
}

func TestSessionActive(t *testing.T) {
	s := store(t)
	u := mkUser(t, s)
	d, sess, rt := login(t, s, u.ID, 10)
	ctx := context.Background()
	active := func() bool {
		ok, err := s.SessionActive(ctx, u.ID, sess.FamilyID, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !active() {
		t.Fatal("fresh session must be active")
	}
	if res, _ := rotate(t, s, rt); res.Outcome != domain.RotateOK || !active() {
		t.Fatal("a rotated session stays active")
	}
	other := mkUser(t, s)
	if ok, _ := s.SessionActive(ctx, other.ID, sess.FamilyID, d.ID); ok {
		t.Error("a session must not be active for a different user")
	}
	if _, err := s.RevokeFamily(ctx, u.ID, sess.FamilyID); err != nil {
		t.Fatal(err)
	}
	if active() {
		t.Error("revoked session must be inactive")
	}

	d2, sess2, _ := login(t, s, u.ID, 10)
	if err := s.RevokeDevice(ctx, u.ID, d2.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SessionActive(ctx, u.ID, sess2.FamilyID, d2.ID); ok {
		t.Error("a revoked device's session must be inactive")
	}

	d3, sess3, _ := login(t, s, u.ID, 10)
	if _, err := pool(t).Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1::uuid`, u.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.SessionActive(ctx, u.ID, sess3.FamilyID, d3.ID); ok {
		t.Error("a disabled user's session must be inactive")
	}
}

func TestMemberIDsAuthorizes(t *testing.T) {
	s := store(t)
	a, b, stranger := mkActor(t, s), mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)
	ids, err := s.MemberIDs(context.Background(), conv, a.user.ID)
	if err != nil || len(ids) != 2 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if _, err := s.MemberIDs(context.Background(), conv, stranger.user.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("stranger: want not found, got %v", err)
	}
}

func TestMessageTableConstraints(t *testing.T) {
	s := store(t)
	a, b := mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)
	p := pool(t)
	ctx := context.Background()
	ins := func(seq, mod int64, key string, body *string, deleted bool) error {
		del := "NULL"
		if deleted {
			del = "now()"
		}
		_, err := p.Exec(ctx, `INSERT INTO messages (conversation_id, sender_id, sender_device_id, idempotency_key, seq, mod_seq, body, deleted_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, `+del+`)`, conv, a.user.ID, a.dev.ID, key, seq, mod, body)
		return err
	}
	ok, empty := "text", ""
	if err := ins(1, 1, "constraint-key-1", &ok, false); err != nil {
		t.Fatalf("valid insert: %v", err)
	}
	for name, err := range map[string]error{
		"empty body":              ins(2, 2, "constraint-key-2", &empty, false),
		"live message without body": ins(3, 3, "constraint-key-3", nil, false),
		"tombstone with body":     ins(4, 4, "constraint-key-4", &ok, true),
		"mod_seq below seq":       ins(5, 4, "constraint-key-5", &ok, false),
		"short idempotency key":   ins(6, 6, "short", &ok, false),
	} {
		if sqlState(err) != "23514" {
			t.Errorf("%s: want check violation 23514, got %v", name, err)
		}
	}
	if sqlState(ins(1, 7, "constraint-key-8", &ok, false)) != "23505" {
		t.Error("duplicate (conversation, seq) must violate the unique index")
	}
	if sqlState(ins(8, 1, "constraint-key-9", &ok, false)) != "23505" {
		t.Error("duplicate (conversation, mod_seq) must violate the unique index")
	}
	if sqlState(ins(9, 9, "constraint-key-1", &ok, false)) != "23505" {
		t.Error("duplicate (device, idempotency key) must violate the unique index")
	}
}
