package receipts

import (
	"context"
	"errors"
	"testing"

	"realtime-chat/backend/internal/domain"
)

const (
	conv = "11111111-1111-1111-1111-111111111111"
	user = "00000000-0000-0000-0000-00000000000a"
)

type fakeRepo struct {
	states []domain.ReceiptState
	calls  int
	acks   []domain.DeviceAck
	reads  []domain.ReadMark
}

func (f *fakeRepo) ApplyCursors(_ context.Context, a []domain.DeviceAck, r []domain.ReadMark) ([]domain.ReceiptState, error) {
	f.calls++
	f.acks, f.reads = a, r
	return f.states, nil
}
func (f *fakeRepo) ListReceipts(context.Context, string, string) ([]domain.MemberReceipt, error) {
	return []domain.MemberReceipt{{UserID: user, Delivered: 3, Read: 2}}, nil
}

type fakePub struct{ got []domain.ReceiptState }

func (p *fakePub) PublishReceipt(s domain.ReceiptState) { p.got = append(p.got, s) }

func TestApplyPublishesEveryResultingState(t *testing.T) {
	repo := &fakeRepo{states: []domain.ReceiptState{{ConversationID: conv, UserID: user, Delivered: 5, Read: 4}, {ConversationID: "c2", UserID: user}}}
	pub := &fakePub{}
	err := New(repo, pub).Apply(context.Background(), []domain.DeviceAck{{UserID: user, DeviceID: "d", ConversationID: conv, UpToModSeq: 5}}, nil)
	if err != nil || len(pub.got) != 2 || pub.got[0].Delivered != 5 {
		t.Fatalf("err=%v published=%+v", err, pub.got)
	}
}

func TestApplyWithNothingDoesNotTouchTheStore(t *testing.T) {
	repo := &fakeRepo{}
	if err := New(repo, nil).Apply(context.Background(), nil, nil); err != nil || repo.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, repo.calls)
	}
}

func TestMarkReadValidatesAndRejectsStrangers(t *testing.T) {
	repo := &fakeRepo{}
	s := New(repo, nil)
	ctx := context.Background()
	if err := s.MarkRead(ctx, user, "nope", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
	if err := s.MarkRead(ctx, user, conv, -1); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("negative seq: %v", err)
	}
	if repo.calls != 0 {
		t.Error("invalid input must not reach the store")
	}
	// The store reports no updated pair when the caller is not an active member.
	if err := s.MarkRead(ctx, user, conv, 3); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("stranger: want not found, got %v", err)
	}
	repo.states = []domain.ReceiptState{{ConversationID: conv, UserID: user, Read: 3}}
	pub := &fakePub{}
	if err := New(repo, pub).MarkRead(ctx, user, conv, 3); err != nil || len(pub.got) != 1 {
		t.Fatalf("member: err=%v published=%v", err, pub.got)
	}
	if len(repo.reads) != 1 || repo.reads[0].ConversationID != conv || repo.reads[0].UpToSeq != 3 {
		t.Errorf("read mark passed to the store: %+v", repo.reads)
	}
}

func TestListNormalizesTheConversationID(t *testing.T) {
	out, err := New(&fakeRepo{}, nil).List(context.Background(), user, conv)
	if err != nil || len(out) != 1 {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if _, err := New(&fakeRepo{}, nil).List(context.Background(), user, "bad"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("bad id: %v", err)
	}
}
