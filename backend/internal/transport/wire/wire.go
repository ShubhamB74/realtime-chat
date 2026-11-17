// Package wire holds JSON shapes shared by the REST API and the WebSocket protocol.
package wire

import (
	"time"

	"realtime-chat/backend/internal/domain"
)

// Message is the wire form of a message. A deleted message has a null body and deleted_at set.
type Message struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversation_id"`
	SenderID       *string    `json:"sender_id"`
	Seq            int64      `json:"seq"`
	ModSeq         int64      `json:"mod_seq"`
	Body           *string    `json:"body"`
	EditedAt       *time.Time `json:"edited_at"`
	DeletedAt      *time.Time `json:"deleted_at"`
	CreatedAt      time.Time  `json:"created_at"`
	IdempotencyKey string     `json:"idempotency_key,omitempty"`
}

// FromMessage converts a domain message.
func FromMessage(m domain.Message) Message {
	return Message{
		ID: m.ID, ConversationID: m.ConversationID, SenderID: m.SenderID, Seq: m.Seq, ModSeq: m.ModSeq,
		Body: m.Body, EditedAt: m.EditedAt, DeletedAt: m.DeletedAt, CreatedAt: m.CreatedAt,
		IdempotencyKey: m.IdempotencyKey,
	}
}

// FromMessages converts a slice (never nil, so JSON renders [] not null).
func FromMessages(ms []domain.Message) []Message {
	out := make([]Message, 0, len(ms))
	for _, m := range ms {
		out = append(out, FromMessage(m))
	}
	return out
}
