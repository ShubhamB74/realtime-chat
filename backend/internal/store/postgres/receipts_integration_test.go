//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/store/postgres"
)

func directOf(t *testing.T, s *postgres.Store, a, b actor) string {
	t.Helper()
	c, _, err := s.GetOrCreateDirect(context.Background(), a.user.ID, b.user.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool(t).Exec(context.Background(), `DELETE FROM conversations WHERE id = $1::uuid`, c.ID)
	})
	return c.ID
}

func ack(who actor, conv string, n int64) domain.DeviceAck {
	return domain.DeviceAck{UserID: who.user.ID, DeviceID: who.dev.ID, ConversationID: conv, UpToModSeq: n}
}

func apply(t *testing.T, s *postgres.Store, acks []domain.DeviceAck, reads []domain.ReadMark) []domain.ReceiptState {
	t.Helper()
	st, err := s.ApplyCursors(context.Background(), acks, reads)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return st
}

func TestCursorsAreMonotonicCappedAndValidated(t *testing.T) {
	s := store(t)
	a, b, stranger := mkActor(t, s), mkActor(t, s), mkActor(t, s)
	conv := directOf(t, s, a, b)
	for i := 1; i <= 3; i++ {
		send(t, s, conv, a, fmt.Sprintf("cursor-key-%04d", i), "m")
	}

	st := apply(t, s, []domain.DeviceAck{ack(b, conv, 2)}, nil)
	if len(st) != 1 || st[0].Delivered != 2 || !st[0].Direct || len(st[0].Others) != 1 || st[0].Others[0] != a.user.ID {
		t.Fatalf("first ack: %+v", st)
	}
	if st := apply(t, s, []domain.DeviceAck{ack(b, conv, 1)}, nil); st[0].Delivered != 2 {
		t.Errorf("cursors never move backwards: %+v", st)
	}
	if st := apply(t, s, []domain.DeviceAck{ack(b, conv, 999)}, nil); st[0].Delivered != 3 {
		t.Errorf("a cursor beyond the conversation counter is capped at it (3): %+v", st)
	}

	reads := apply(t, s, nil, []domain.ReadMark{{UserID: b.user.ID, ConversationID: conv, UpToSeq: 2}})
	if len(reads) != 1 || reads[0].Read != 2 {
		t.Fatalf("read mark: %+v", reads)
	}
	if st := apply(t, s, nil, []domain.ReadMark{{UserID: b.user.ID, ConversationID: conv, UpToSeq: 999}}); st[0].Read != 3 {
		t.Errorf("read mark capped at the counter: %+v", st)
	}

	// Strangers, and devices that are not the caller's, change nothing.
	if st := apply(t, s, []domain.DeviceAck{ack(stranger, conv, 3)}, []domain.ReadMark{{UserID: stranger.user.ID, ConversationID: conv, UpToSeq: 3}}); len(st) != 0 {
		t.Errorf("a non-member must not move cursors: %+v", st)
	}
	foreign := domain.DeviceAck{UserID: b.user.ID, DeviceID: stranger.dev.ID, ConversationID: conv, UpToModSeq: 3}
	if st := apply(t, s, []domain.DeviceAck{foreign}, nil); len(st) != 0 {
		t.Errorf("another user's device must be refused: %+v", st)
	}
	var rows int
	_ = pool(t).QueryRow(context.Background(), `SELECT count(*) FROM device_cursors WHERE device_id = $1::uuid`, stranger.dev.ID).Scan(&rows)
	if rows != 0 {
		t.Errorf("no cursor row may be created for a refused ack, got %d", rows)
	}

	// Delivered is the highest cursor across the user's live devices; revoked devices do not count.
	d2, _, _ := login(t, s, b.user.ID, 10)
	second := actor{user: b.user, dev: d2}
	apply(t, s, []domain.DeviceAck{ack(second, conv, 1)}, nil) // lower than the first device's 3
	if err := s.RevokeDevice(context.Background(), b.user.ID, b.dev.ID); err != nil {
		t.Fatal(err)
	}
	if st := apply(t, s, []domain.DeviceAck{ack(second, conv, 1)}, nil); st[0].Delivered != 1 {
		t.Errorf("a revoked device's cursor must not count: %+v", st)
	}
}

func TestGroupReceiptStateOmitsOthers(t *testing.T) {
	s := store(t)
	a, b := mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)
	send(t, s, conv, a, "grp-rcpt-00001", "m")
	st := apply(t, s, []domain.DeviceAck{ack(b, conv, 1)}, nil)
	if len(st) != 1 || st[0].Direct || len(st[0].Others) != 0 {
		t.Fatalf("group receipt state: %+v", st)
	}
}

func TestUnreadCountsInTheConversationList(t *testing.T) {
	s := store(t)
	a, b := mkActor(t, s), mkActor(t, s)
	conv := directOf(t, s, a, b)
	ctx := context.Background()
	unread := func(who actor) int {
		l, _, err := s.ListForUser(ctx, who.user.ID, nil, 10)
		if err != nil || len(l) != 1 {
			t.Fatalf("list: %v %v", l, err)
		}
		return l[0].Unread
	}

	m1 := send(t, s, conv, a, "unread-key-0001", "one").Message
	send(t, s, conv, a, "unread-key-0002", "two")
	send(t, s, conv, a, "unread-key-0003", "three")
	send(t, s, conv, b, "unread-key-0004", "b's own message")
	if got := unread(b); got != 3 {
		t.Fatalf("b's unread = %d, want 3 (own messages never count)", got)
	}
	if got := unread(a); got != 1 {
		t.Fatalf("a's unread = %d, want 1 (only b's message)", got)
	}
	apply(t, s, nil, []domain.ReadMark{{UserID: b.user.ID, ConversationID: conv, UpToSeq: 2}})
	if got := unread(b); got != 1 {
		t.Errorf("after reading up to 2, unread = %d, want 1", got)
	}
	if _, err := s.DeleteMessage(ctx, conv, m1.ID, a.user.ID); err != nil {
		t.Fatal(err)
	}
	if got := unread(a); got != 1 {
		t.Errorf("a's unread after deleting their own message = %d", got)
	}

	// The count is capped so listing stays cheap.
	flooder := mkActor(t, s)
	c2 := directOf(t, s, flooder, b)
	for i := 0; i < domain.UnreadCap+5; i++ {
		send(t, s, c2, flooder, fmt.Sprintf("flood-key-%05d", i), "x")
	}
	l, _, _ := s.ListForUser(ctx, b.user.ID, nil, 10)
	capped := false
	for _, c := range l {
		if c.ID == c2 {
			capped = c.Unread == domain.UnreadCap
		}
	}
	if !capped {
		t.Errorf("unread must be capped at %d", domain.UnreadCap)
	}
}

func TestListReceiptsShowsEveryMembersPosition(t *testing.T) {
	s := store(t)
	a, b, stranger := mkActor(t, s), mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)
	send(t, s, conv, a, "seen-by-000001", "m1")
	send(t, s, conv, a, "seen-by-000002", "m2")
	apply(t, s, []domain.DeviceAck{ack(b, conv, 2)}, []domain.ReadMark{{UserID: b.user.ID, ConversationID: conv, UpToSeq: 1}})

	got, err := s.ListReceipts(context.Background(), conv, a.user.ID)
	if err != nil || len(got) != 2 {
		t.Fatalf("receipts=%v err=%v", got, err)
	}
	for _, r := range got {
		switch r.UserID {
		case b.user.ID:
			if r.Delivered != 2 || r.Read != 1 {
				t.Errorf("b: %+v", r)
			}
		case a.user.ID:
			if r.Delivered != 0 || r.Read != 0 {
				t.Errorf("a: %+v", r)
			}
		}
	}
	if _, err := s.ListReceipts(context.Background(), conv, stranger.user.ID); err == nil {
		t.Error("a stranger must not see receipts")
	}
}

func TestPendingConversationsDrivenByDeviceCursors(t *testing.T) {
	s := store(t)
	a, b, c := mkActor(t, s), mkActor(t, s), mkActor(t, s)
	ctx := context.Background()
	conv1 := mkGroupOf(t, s, a, b)
	conv2 := mkGroupOf(t, s, a, b) // stays empty

	pending := func(who actor) []domain.PendingSync {
		p, err := s.PendingConversations(ctx, who.user.ID, who.dev.ID, 20)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := pending(b); len(p) != 0 {
		t.Fatalf("nothing has been sent yet: %+v", p)
	}
	m1 := send(t, s, conv1, a, "pending-key-001", "one").Message
	send(t, s, conv1, a, "pending-key-002", "two")
	p := pending(b)
	if len(p) != 1 || p[0].ConversationID != conv1 || p[0].Cursor != 0 {
		t.Fatalf("b should have conv1 pending from cursor 0 (conv2 is empty): %+v", p)
	}
	_ = conv2

	apply(t, s, []domain.DeviceAck{ack(b, conv1, 2)}, nil)
	if p := pending(b); len(p) != 0 {
		t.Fatalf("after acknowledging everything nothing is pending: %+v", p)
	}
	send(t, s, conv1, a, "pending-key-003", "three")
	if p := pending(b); len(p) != 1 || p[0].Cursor != 2 {
		t.Fatalf("pending resumes from the acknowledged cursor: %+v", p)
	}
	// Another device of the same user has its own cursor.
	d2, _, _ := login(t, s, b.user.ID, 10)
	if p, _ := s.PendingConversations(ctx, b.user.ID, d2.ID, 20); len(p) != 1 || p[0].Cursor != 0 {
		t.Fatalf("a device that never acknowledged starts at the join point: %+v", p)
	}

	// A member who joined later: invisible edits of old messages still bump the counter, so the
	// conversation shows up as pending, yields no visible change, and clears once the device
	// acknowledges the counter value that sync reported (the documented protocol).
	if _, err := s.AddMembers(ctx, conv1, []string{c.user.ID}, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditMessage(ctx, conv1, m1.ID, a.user.ID, "one, edited"); err != nil {
		t.Fatal(err)
	}
	if p := pending(c); len(p) != 1 || p[0].Cursor != 3 {
		t.Fatalf("c should have conv1 pending from its join point (3): %+v", p)
	}
	ch, err := s.ListChanges(ctx, conv1, c.user.ID, 3, 50)
	if err != nil || len(ch.Messages) != 0 || ch.LastSeq != 4 {
		t.Fatalf("the edit of a pre-join message is invisible to c: %+v err=%v", ch, err)
	}
	apply(t, s, []domain.DeviceAck{ack(c, conv1, ch.LastSeq)}, nil)
	if p := pending(c); len(p) != 0 {
		t.Fatalf("acknowledging the reported last_seq must clear it: %+v", p)
	}

	// Removed members have nothing pending.
	if err := s.RemoveMember(ctx, conv1, b.user.ID); err != nil {
		t.Fatal(err)
	}
	if p := pending(b); len(p) != 0 {
		t.Fatalf("a removed member has no pending conversations: %+v", p)
	}
}

func TestVisibleUsersAuthorizesPresenceSubscriptions(t *testing.T) {
	s := store(t)
	a, b, c, d := mkActor(t, s), mkActor(t, s), mkActor(t, s), mkActor(t, s)
	e, f, g := mkActor(t, s), mkActor(t, s), mkActor(t, s)
	ctx := context.Background()
	small := mkGroupOf(t, s, a, b, c)  // 3 members
	_ = mkGroupOf(t, s, a, d, e, f, g) // 5 members

	ids := func(users ...actor) []string {
		out := make([]string, 0, len(users))
		for _, u := range users {
			out = append(out, u.user.ID)
		}
		return out
	}
	set := func(got []string) map[string]bool {
		m := map[string]bool{}
		for _, u := range got {
			m[u] = true
		}
		return m
	}

	got, err := s.VisibleUsers(ctx, a.user.ID, ids(a, b, d), 10)
	if err != nil {
		t.Fatal(err)
	}
	if m := set(got); !m[b.user.ID] || !m[d.user.ID] || m[a.user.ID] {
		t.Fatalf("visible=%v: shared groups make b and d visible, never the viewer", got)
	}
	// Threshold 4: the 3-person group still counts, the 5-person group must not.
	got, _ = s.VisibleUsers(ctx, a.user.ID, ids(b, d), 4)
	if m := set(got); !m[b.user.ID] || m[d.user.ID] {
		t.Fatalf("visible=%v: presence must not be exposed through groups larger than the threshold", got)
	}
	if got, _ := s.VisibleUsers(ctx, mkActor(t, s).user.ID, ids(a, b, d), 10); len(got) != 0 {
		t.Errorf("a user with no shared conversation sees nobody: %v", got)
	}
	if err := s.RemoveMember(ctx, small, b.user.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.VisibleUsers(ctx, a.user.ID, ids(b), 10); len(got) != 0 {
		t.Errorf("a removed member is no longer visible: %v", got)
	}
}

// The counter is read before the changes and bounds the query, so last_seq is always safe to
// acknowledge once has_more is false, even while writers are running (the race fixed in 4A).
func TestLastSeqIsASafeCursorUnderConcurrentWrites(t *testing.T) {
	s := store(t)
	a, b := mkActor(t, s), mkActor(t, s)
	conv := mkGroupOf(t, s, a, b)
	ctx := context.Background()

	const total = 60
	var wg sync.WaitGroup
	wg.Add(1)
	done := make(chan struct{})
	go func() {
		defer wg.Done()
		defer close(done)
		for i := 0; i < total; i++ {
			if _, err := s.SendMessage(ctx, domain.SendParams{ConversationID: conv, UserID: a.user.ID, DeviceID: a.dev.ID,
				IdempotencyKey: fmt.Sprintf("safe-cursor-%04d", i), Body: "m"}); err != nil {
				t.Errorf("send: %v", err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	got := map[int64]bool{}
	var cursor int64
	finished := false
	for iter := 0; iter < 5000 && !(finished && int(cursor) >= total); iter++ {
		select {
		case <-done:
			finished = true
		default:
		}
		ch, err := s.ListChanges(ctx, conv, b.user.ID, cursor, 7) // small pages to exercise has_more
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ch.Messages {
			got[m.Seq] = true
			cursor = max(cursor, m.ModSeq)
		}
		if !ch.HasMore {
			cursor = max(cursor, ch.LastSeq) // exactly what a client does after a complete batch
		}
	}
	wg.Wait()
	for i := int64(1); i <= total; i++ {
		if !got[i] {
			t.Fatalf("message seq %d was skipped: acknowledging last_seq lost a concurrent change", i)
		}
	}
}
