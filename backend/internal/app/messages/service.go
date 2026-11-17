// Package messages implements sending, editing, deleting and reading messages.
//
// Persistence comes first: a message is committed to PostgreSQL before Send returns, and only
// then is the best-effort live notification published (ADR-003, ADR-005). Publishing can never
// fail or block the caller; clients that miss a push converge through sync.
package messages

import (
	"context"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"realtime-chat/backend/internal/app/chats"
	"realtime-chat/backend/internal/domain"
)

const (
	MaxBodyRunes = 4000

	DefaultHistoryLimit = 50
	MaxHistoryLimit     = 100
	DefaultChangesLimit = 100
	MaxChangesLimit     = 200

	EventCreated = "message.created"
	EventUpdated = "message.updated"
)

var idempotencyKeyRe = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// Repo is the persistence the service needs (implemented by postgres.Store).
type Repo interface {
	SendMessage(ctx context.Context, p domain.SendParams) (domain.MessageResult, error)
	EditMessage(ctx context.Context, convID, msgID, userID, body string) (domain.MessageResult, error)
	DeleteMessage(ctx context.Context, convID, msgID, userID string) (domain.MessageResult, error)
	ListHistory(ctx context.Context, convID, userID string, beforeSeq int64, limit int) ([]domain.Message, bool, error)
	ListChanges(ctx context.Context, convID, userID string, afterModSeq int64, limit int) (domain.Changes, error)
	MemberIDs(ctx context.Context, convID, userID string) ([]string, error)
}

// Event describes a committed change for live delivery.
type Event struct {
	Type       string // EventCreated or EventUpdated
	Recipients []string
	Message    domain.Message
	// OriginConn is the WebSocket connection that made the change (empty for REST). It receives
	// an acknowledgement instead of the broadcast, so it is excluded from the fan-out.
	OriginConn string
}

// Publisher delivers events to connected clients. Implementations must not block and must not
// fail the caller; delivery is best effort.
type Publisher interface{ Publish(Event) }

// Service holds message use cases.
type Service struct {
	repo Repo
	pub  Publisher
}

// New builds the service. pub may be nil (no live delivery).
func New(repo Repo, pub Publisher) *Service { return &Service{repo: repo, pub: pub} }

func (s *Service) publish(e Event) {
	if s.pub != nil {
		s.pub.Publish(e)
	}
}

// ValidateBody checks message text: 1 to 4000 characters, not blank, no control characters
// other than newline, carriage return and tab. The text is stored as given (not trimmed).
func ValidateBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return domain.Invalid("Message body must not be empty")
	}
	if utf8.RuneCountInString(body) > MaxBodyRunes {
		return domain.Invalid("Message body is too long")
	}
	if !utf8.ValidString(body) {
		return domain.Invalid("Message body must be valid UTF-8")
	}
	for _, r := range body {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return domain.Invalid("Message body contains invalid characters")
		}
	}
	return nil
}

// ValidateIdempotencyKey checks the client-generated retry key.
func ValidateIdempotencyKey(k string) error {
	if !idempotencyKeyRe.MatchString(k) {
		return domain.Invalid("idempotency_key must be 8 to 64 characters of letters, digits, dot, dash or underscore")
	}
	return nil
}

type SendInput struct {
	UserID         string
	DeviceID       string
	ConversationID string
	IdempotencyKey string
	Body           string
	OriginConn     string
}

type SendOutput struct {
	Message   domain.Message
	Duplicate bool
}

// Send persists a message from an authenticated device and publishes it to the members.
func (s *Service) Send(ctx context.Context, in SendInput) (SendOutput, error) {
	convID, err := chats.NormalizeID(in.ConversationID)
	if err != nil {
		return SendOutput{}, err
	}
	if err := ValidateIdempotencyKey(in.IdempotencyKey); err != nil {
		return SendOutput{}, err
	}
	if err := ValidateBody(in.Body); err != nil {
		return SendOutput{}, err
	}
	res, err := s.repo.SendMessage(ctx, domain.SendParams{
		ConversationID: convID, UserID: in.UserID, DeviceID: in.DeviceID,
		IdempotencyKey: in.IdempotencyKey, Body: in.Body,
	})
	if err != nil {
		return SendOutput{}, err
	}
	if !res.Duplicate {
		s.publish(Event{Type: EventCreated, Recipients: res.Recipients, Message: res.Message, OriginConn: in.OriginConn})
	}
	return SendOutput{Message: res.Message, Duplicate: res.Duplicate}, nil
}

// Edit replaces the body of the caller's own message.
func (s *Service) Edit(ctx context.Context, userID, convID, msgID, body string) (domain.Message, error) {
	cid, err := chats.NormalizeID(convID)
	if err != nil {
		return domain.Message{}, err
	}
	mid, err := chats.NormalizeID(msgID)
	if err != nil {
		return domain.Message{}, err
	}
	if err := ValidateBody(body); err != nil {
		return domain.Message{}, err
	}
	res, err := s.repo.EditMessage(ctx, cid, mid, userID, body)
	if err != nil {
		return domain.Message{}, err
	}
	s.publish(Event{Type: EventUpdated, Recipients: res.Recipients, Message: res.Message})
	return res.Message, nil
}

// Delete tombstones a message (sender, or a group admin).
func (s *Service) Delete(ctx context.Context, userID, convID, msgID string) (domain.Message, error) {
	cid, err := chats.NormalizeID(convID)
	if err != nil {
		return domain.Message{}, err
	}
	mid, err := chats.NormalizeID(msgID)
	if err != nil {
		return domain.Message{}, err
	}
	res, err := s.repo.DeleteMessage(ctx, cid, mid, userID)
	if err != nil {
		return domain.Message{}, err
	}
	if !res.Duplicate {
		s.publish(Event{Type: EventUpdated, Recipients: res.Recipients, Message: res.Message})
	}
	return res.Message, nil
}

func clamp(n, def, max int) int {
	switch {
	case n <= 0:
		return def
	case n > max:
		return max
	}
	return n
}

// History returns messages newest first, older than beforeSeq (0 = latest).
func (s *Service) History(ctx context.Context, userID, convID string, beforeSeq int64, limit int) ([]domain.Message, bool, error) {
	cid, err := chats.NormalizeID(convID)
	if err != nil {
		return nil, false, err
	}
	if beforeSeq < 0 {
		return nil, false, domain.Invalid("before_seq must not be negative")
	}
	return s.repo.ListHistory(ctx, cid, userID, beforeSeq, clamp(limit, DefaultHistoryLimit, MaxHistoryLimit))
}

// Changes returns every message created, edited or deleted after the caller's cursor.
func (s *Service) Changes(ctx context.Context, userID, convID string, afterModSeq int64, limit int) (domain.Changes, error) {
	cid, err := chats.NormalizeID(convID)
	if err != nil {
		return domain.Changes{}, err
	}
	if afterModSeq < 0 {
		return domain.Changes{}, domain.Invalid("after_mod_seq must not be negative")
	}
	return s.repo.ListChanges(ctx, cid, userID, afterModSeq, clamp(limit, DefaultChangesLimit, MaxChangesLimit))
}

// Recipients authorizes the caller and returns the conversation's active member ids.
func (s *Service) Recipients(ctx context.Context, userID, convID string) ([]string, error) {
	cid, err := chats.NormalizeID(convID)
	if err != nil {
		return nil, err
	}
	return s.repo.MemberIDs(ctx, cid, userID)
}
