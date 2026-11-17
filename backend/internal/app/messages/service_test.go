package messages

import (
	"context"
	"errors"
	"strings"
	"testing"

	"realtime-chat/backend/internal/domain"
)

const (
	conv = "11111111-1111-1111-1111-111111111111"
	msg  = "33333333-3333-3333-3333-333333333333"
	user = "00000000-0000-0000-0000-00000000000a"
	dev  = "44444444-4444-4444-4444-444444444444"
	key  = "client-key-0001"
)

type fakeRepo struct {
	Repo
	sendRes  domain.MessageResult
	sendErr  error
	sent     *domain.SendParams
	editRes  domain.MessageResult
	delRes   domain.MessageResult
	histLim  int
	chgLim   int
	histBack int64
}

func (f *fakeRepo) SendMessage(_ context.Context, p domain.SendParams) (domain.MessageResult, error) {
	f.sent = &p
	return f.sendRes, f.sendErr
}
func (f *fakeRepo) EditMessage(context.Context, string, string, string, string) (domain.MessageResult, error) {
	return f.editRes, nil
}
func (f *fakeRepo) DeleteMessage(context.Context, string, string, string) (domain.MessageResult, error) {
	return f.delRes, nil
}
func (f *fakeRepo) ListHistory(_ context.Context, _, _ string, before int64, limit int) ([]domain.Message, bool, error) {
	f.histLim, f.histBack = limit, before
	return nil, false, nil
}
func (f *fakeRepo) ListChanges(_ context.Context, _, _ string, _ int64, limit int) (domain.Changes, error) {
	f.chgLim = limit
	return domain.Changes{}, nil
}

type fakePub struct{ events []Event }

func (p *fakePub) Publish(e Event) { p.events = append(p.events, e) }

func input() SendInput {
	return SendInput{UserID: user, DeviceID: dev, ConversationID: strings.ToUpper(conv), IdempotencyKey: key, Body: "hello", OriginConn: "conn-1"}
}

func TestSendPublishesAfterPersisting(t *testing.T) {
	body := "hello"
	repo := &fakeRepo{sendRes: domain.MessageResult{Message: domain.Message{ID: msg, ConversationID: conv, Seq: 1, ModSeq: 1, Body: &body}, Recipients: []string{user, "u2"}}}
	pub := &fakePub{}
	out, err := New(repo, pub).Send(context.Background(), input())
	if err != nil || out.Duplicate || out.Message.Seq != 1 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if repo.sent == nil || repo.sent.ConversationID != conv {
		t.Errorf("conversation id must be normalized to lowercase: %+v", repo.sent)
	}
	if len(pub.events) != 1 {
		t.Fatalf("want 1 event, got %d", len(pub.events))
	}
	e := pub.events[0]
	if e.Type != EventCreated || e.OriginConn != "conn-1" || len(e.Recipients) != 2 {
		t.Errorf("event = %+v", e)
	}
}

func TestSendDuplicateDoesNotRepublish(t *testing.T) {
	repo := &fakeRepo{sendRes: domain.MessageResult{Duplicate: true, Message: domain.Message{ID: msg}}}
	pub := &fakePub{}
	out, err := New(repo, pub).Send(context.Background(), input())
	if err != nil || !out.Duplicate {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if len(pub.events) != 0 {
		t.Error("an idempotent retry must not be broadcast again")
	}
}

func TestSendFailureDoesNotPublish(t *testing.T) {
	repo := &fakeRepo{sendErr: domain.NotFound()}
	pub := &fakePub{}
	if _, err := New(repo, pub).Send(context.Background(), input()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if len(pub.events) != 0 {
		t.Error("nothing may be published for a failed send")
	}
}

func TestSendValidationRejectsBeforeTouchingStore(t *testing.T) {
	cases := map[string]func(*SendInput){
		"bad conversation id": func(i *SendInput) { i.ConversationID = "nope" },
		"short key":           func(i *SendInput) { i.IdempotencyKey = "short" },
		"key with spaces":     func(i *SendInput) { i.IdempotencyKey = "has spaces in it" },
		"empty body":          func(i *SendInput) { i.Body = "" },
		"blank body":          func(i *SendInput) { i.Body = " \n\t " },
		"too long":            func(i *SendInput) { i.Body = strings.Repeat("x", MaxBodyRunes+1) },
		"control char":        func(i *SendInput) { i.Body = "bad\x00char" },
		"invalid utf8":        func(i *SendInput) { i.Body = "bad\xffbytes" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			repo := &fakeRepo{}
			in := input()
			mutate(&in)
			_, err := New(repo, nil).Send(context.Background(), in)
			if !errors.Is(err, domain.ErrInvalid) && !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("want invalid/not found, got %v", err)
			}
			if repo.sent != nil {
				t.Error("store must not be called")
			}
		})
	}
}

func TestBodyBoundaries(t *testing.T) {
	for _, ok := range []string{"x", "multi\nline\r\nwith\ttab", strings.Repeat("é", MaxBodyRunes), "emoji 🙂"} {
		if err := ValidateBody(ok); err != nil {
			t.Errorf("%q rejected: %v", ok[:min(len(ok), 20)], err)
		}
	}
}

func TestEditAndDeletePublishUpdates(t *testing.T) {
	body := "edited"
	repo := &fakeRepo{
		editRes: domain.MessageResult{Message: domain.Message{ID: msg, ModSeq: 5, Body: &body}, Recipients: []string{user}},
		delRes:  domain.MessageResult{Message: domain.Message{ID: msg, ModSeq: 6}, Recipients: []string{user}},
	}
	pub := &fakePub{}
	s := New(repo, pub)
	if _, err := s.Edit(context.Background(), user, conv, msg, "edited"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(context.Background(), user, conv, msg); err != nil {
		t.Fatal(err)
	}
	if len(pub.events) != 2 || pub.events[0].Type != EventUpdated || pub.events[1].Message.ModSeq != 6 {
		t.Fatalf("events = %+v", pub.events)
	}
	if _, err := s.Edit(context.Background(), user, conv, msg, "  "); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("blank edit: %v", err)
	}
}

func TestDeleteNoOpDoesNotPublish(t *testing.T) {
	repo := &fakeRepo{delRes: domain.MessageResult{Duplicate: true, Message: domain.Message{ID: msg}}}
	pub := &fakePub{}
	if _, err := New(repo, pub).Delete(context.Background(), user, conv, msg); err != nil {
		t.Fatal(err)
	}
	if len(pub.events) != 0 {
		t.Error("deleting an already deleted message must not publish")
	}
}

func TestReadLimitsAreClampedAndValidated(t *testing.T) {
	repo := &fakeRepo{}
	s := New(repo, nil)
	ctx := context.Background()
	_, _, _ = s.History(ctx, user, conv, 0, 0)
	if repo.histLim != DefaultHistoryLimit {
		t.Errorf("default history limit = %d", repo.histLim)
	}
	_, _, _ = s.History(ctx, user, conv, 0, 1<<20)
	if repo.histLim != MaxHistoryLimit {
		t.Errorf("capped history limit = %d", repo.histLim)
	}
	_, _ = s.Changes(ctx, user, conv, 0, 1<<20)
	if repo.chgLim != MaxChangesLimit {
		t.Errorf("capped changes limit = %d", repo.chgLim)
	}
	if _, _, err := s.History(ctx, user, conv, -1, 10); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("negative before_seq: %v", err)
	}
	if _, err := s.Changes(ctx, user, conv, -1, 10); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("negative after_mod_seq: %v", err)
	}
	if _, _, err := s.History(ctx, user, "bad", 0, 10); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("bad conversation id: %v", err)
	}
}
