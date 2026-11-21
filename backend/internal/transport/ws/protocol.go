// Package ws is the WebSocket gateway: handshake authentication, connection lifecycle,
// bounded per-connection queues, heartbeats and the v1 event protocol (docs/websocket-protocol.md).
package ws

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/coder/websocket"

	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/transport/wire"
)

const (
	// Subprotocol is negotiated at the handshake. Browsers, which cannot set headers on a
	// WebSocket, pass the access token as a second offered subprotocol: "bearer.<token>".
	Subprotocol     = "rtc.v1"
	ProtocolVersion = 1

	// Application close codes (4000-4999 range).
	CloseSessionRevoked websocket.StatusCode = 4001
	CloseSlowConsumer   websocket.StatusCode = 4002
)

// Client event types implemented in this version.
const (
	EvMessageSend         = "message.send"
	EvMessageAck          = "message.ack"
	EvReadUpdate          = "read.update"
	EvSyncRequest         = "sync.request"
	EvTypingStart         = "typing.start"
	EvTypingStop          = "typing.stop"
	EvPresenceSubscribe   = "presence.subscribe"
	EvPresenceUnsubscribe = "presence.unsubscribe"
	evBearerPrefix        = "bearer."
)

// Event types specified in the LLD but not implemented yet. They are answered with an
// unsupported_event error so clients can feature-detect.
var plannedEvents = map[string]string{
	"presence.update":    "not planned: presence follows connection liveness",
	"conversation.join":  "not needed: members receive their conversations' events automatically",
	"conversation.leave": "not needed: members receive their conversations' events automatically",
}

// Server event types.
const (
	EvReady            = "connection.ready"
	EvAccepted         = "message.accepted"
	EvReceived         = "message.received"
	EvUpdated          = "message.updated"
	EvTyping           = "typing.update"
	EvSyncBatch        = "sync.batch"
	EvReceipt          = "receipt.updated"
	EvPresenceSnapshot = "presence.snapshot"
	EvPresenceChanged  = "presence.changed"
	EvError            = "error"
)

// Envelope wraps every frame in both directions.
type Envelope struct {
	V         int             `json:"v"`
	Type      string          `json:"type"`
	EventID   string          `json:"event_id,omitempty"`
	RequestID string          `json:"request_id,omitempty"`
	TS        string          `json:"ts,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// ---- client payloads --------------------------------------------------------------------------

type sendPayload struct {
	ConversationID string `json:"conversation_id"`
	IdempotencyKey string `json:"idempotency_key"`
	Body           string `json:"body"`
}

type syncItem struct {
	ConversationID string `json:"conversation_id"`
	AfterModSeq    int64  `json:"after_mod_seq"`
}

// syncPayload: when Conversations is absent or null the server decides which conversations to
// sync from the device's acknowledged cursors (server-driven sync, ADR-005); an explicit empty
// list is invalid.
type syncPayload struct {
	Conversations *[]syncItem `json:"conversations"`
}

type ackPayload struct {
	ConversationID string `json:"conversation_id"`
	UpToModSeq     int64  `json:"up_to_mod_seq"`
}

type readPayload struct {
	ConversationID string `json:"conversation_id"`
	UpToSeq        int64  `json:"up_to_seq"`
}

type presenceSubPayload struct {
	UserIDs []string `json:"user_ids"`
}

type typingPayload struct {
	ConversationID string `json:"conversation_id"`
}

// ---- server payloads --------------------------------------------------------------------------

type readyPayload struct {
	ConnectionID        string `json:"connection_id"`
	UserID              string `json:"user_id"`
	DeviceID            string `json:"device_id"`
	HeartbeatIntervalMS int64  `json:"heartbeat_interval_ms"`
	MaxFrameBytes       int64  `json:"max_frame_bytes"`
	ServerTime          string `json:"server_time"`
}

type acceptedPayload struct {
	Message   wire.Message `json:"message"`
	Duplicate bool         `json:"duplicate"`
}

type typingUpdate struct {
	ConversationID string `json:"conversation_id"`
	UserID         string `json:"user_id"`
	Typing         bool   `json:"typing"`
}

type syncBatch struct {
	ConversationID string         `json:"conversation_id"`
	Status         string         `json:"status"` // "ok" or "not_found"
	Changes        []wire.Message `json:"changes"`
	HasMore        bool           `json:"has_more"`
	LastSeq        int64          `json:"last_seq"`
	Done           bool           `json:"done"` // true on the last batch of a request
}

type receiptUpdate struct {
	ConversationID string `json:"conversation_id"`
	UserID         string `json:"user_id"`
	DeliveredSeq   int64  `json:"delivered_seq"`
	ReadSeq        int64  `json:"read_seq"`
}

type presenceEntry struct {
	UserID string `json:"user_id"`
	Online bool   `json:"online"`
}

type presenceSnapshot struct {
	Available bool            `json:"available"`
	Users     []presenceEntry `json:"users"`
}

type presenceChanged struct {
	UserID string `json:"user_id"`
	Online bool   `json:"online"`
}

type errorPayload struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "evt-unavailable"
	}
	return hex.EncodeToString(b)
}

// newEvent marshals a server event.
func newEvent(typ, requestID string, payload any) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{
		V: ProtocolVersion, Type: typ, EventID: newID(), RequestID: requestID,
		TS: time.Now().UTC().Format(time.RFC3339Nano), Payload: raw,
	})
}

// classify maps an error to a client-safe code and message. Unknown errors are generic.
func classify(err error) (code, msg string, retryable bool) {
	var de *domain.Error
	m := ""
	if errors.As(err, &de) {
		m = de.Msg
	}
	switch {
	case errors.Is(err, domain.ErrInvalid):
		return "invalid_request", m, false
	case errors.Is(err, domain.ErrForbidden):
		return "forbidden", m, false
	case errors.Is(err, domain.ErrNotFound):
		return "not_found", "Resource not found", false
	case errors.Is(err, domain.ErrConflict):
		return "conflict", m, false
	case errors.Is(err, domain.ErrUnauthenticated):
		return "unauthenticated", m, false
	default:
		return "internal", "Internal server error", false
	}
}
