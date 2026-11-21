// Package receipts implements delivery and read cursors (ADR-007).
//
// Receipts are cursors, not rows: a device acknowledges "I applied every change up to N" and a
// member marks "I have read up to message seq N". Status of an individual message is derived by
// comparing its seq with a cursor. Cursors only move forward and are validated in the database.
package receipts

import (
	"context"

	"realtime-chat/backend/internal/app/chats"
	"realtime-chat/backend/internal/domain"
)

// Repo is the persistence the service needs (implemented by postgres.Store).
type Repo interface {
	ApplyCursors(ctx context.Context, acks []domain.DeviceAck, reads []domain.ReadMark) ([]domain.ReceiptState, error)
	ListReceipts(ctx context.Context, convID, userID string) ([]domain.MemberReceipt, error)
}

// Publisher pushes receipt changes to connected clients (best effort, never blocks).
type Publisher interface{ PublishReceipt(domain.ReceiptState) }

// Service holds receipt use cases.
type Service struct {
	repo Repo
	pub  Publisher
}

// New builds the service. pub may be nil.
func New(repo Repo, pub Publisher) *Service { return &Service{repo: repo, pub: pub} }

// Apply persists a batch of acknowledgements and read marks and publishes the resulting
// states. It is what the gateway's coalescing flusher calls.
func (s *Service) Apply(ctx context.Context, acks []domain.DeviceAck, reads []domain.ReadMark) error {
	if len(acks) == 0 && len(reads) == 0 {
		return nil
	}
	states, err := s.repo.ApplyCursors(ctx, acks, reads)
	if err != nil {
		return err
	}
	if s.pub != nil {
		for _, st := range states {
			s.pub.PublishReceipt(st)
		}
	}
	return nil
}

// MarkRead moves the caller's read cursor (REST path; the WebSocket path is coalesced).
func (s *Service) MarkRead(ctx context.Context, userID, convID string, upToSeq int64) error {
	cid, err := chats.NormalizeID(convID)
	if err != nil {
		return err
	}
	if upToSeq < 0 {
		return domain.Invalid("up_to_seq must not be negative")
	}
	// Applied immediately; authorization (active member) is enforced inside the update, so a
	// stranger's request changes nothing. Report that as not found, like everywhere else.
	states, err := s.repo.ApplyCursors(ctx, nil, []domain.ReadMark{{UserID: userID, ConversationID: cid, UpToSeq: upToSeq}})
	if err != nil {
		return err
	}
	if len(states) == 0 {
		return domain.NotFound()
	}
	if s.pub != nil {
		for _, st := range states {
			s.pub.PublishReceipt(st)
		}
	}
	return nil
}

// List returns every member's delivered and read position ("seen by"). Members only.
func (s *Service) List(ctx context.Context, userID, convID string) ([]domain.MemberReceipt, error) {
	cid, err := chats.NormalizeID(convID)
	if err != nil {
		return nil, err
	}
	return s.repo.ListReceipts(ctx, cid, userID)
}
