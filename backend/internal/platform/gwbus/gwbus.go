// Package gwbus defines how gateway processes find each other and exchange live events
// (ADR-006): a connection registry ("which gateway holds a connection of user X?") and a
// message bus (per-gateway channels plus one broadcast channel). Both are best effort and
// non-authoritative: when they fail, clients converge through sync (ADR-005).
package gwbus

import (
	"context"
	"time"
)

// Message kinds.
const (
	KindDeliver  = "deliver"  // push a ready-made WebSocket frame to some users' connections
	KindPresence = "presence" // a user went online or offline (sent to every gateway)
)

// Message is the unit carried by the bus. It is JSON-encoded on the wire.
type Message struct {
	Kind   string   `json:"k"`
	Users  []string `json:"u,omitempty"` // deliver: recipients that have connections on the target gateway
	Except string   `json:"x,omitempty"` // deliver: connection id that must not receive the frame
	Frame  []byte   `json:"f,omitempty"` // deliver: the exact frame to write (base64 in JSON)
	User   string   `json:"p,omitempty"` // presence: the user
	Online bool     `json:"o,omitempty"` // presence: the new state
}

// Bus carries messages between gateways. Delivery is at most once and unordered across
// publishers; there is no persistence and no backpressure (pub/sub semantics).
type Bus interface {
	// Publish sends to one gateway's channel.
	Publish(ctx context.Context, gatewayID string, m Message) error
	// Broadcast sends to every subscribed gateway, including the sender.
	Broadcast(ctx context.Context, m Message) error
	// Subscribe delivers messages for gatewayID and broadcasts to h, in arrival order, until ctx
	// is done (returns nil) or the subscription fails (returns the error; callers retry).
	Subscribe(ctx context.Context, gatewayID string, h func(Message)) error
}

// Registry records live connections so any gateway can find the others' recipients. A
// connection is identified by an opaque member string "<gatewayID>/<connectionID>"; entries
// expire unless refreshed, so a crashed gateway's entries disappear on their own.
type Registry interface {
	Touch(ctx context.Context, userID, member string, ttl time.Duration) error
	Remove(ctx context.Context, userID, member string) error
	// Gateways maps gateway id -> the users (from userIDs) with a live connection there.
	Gateways(ctx context.Context, userIDs []string) (map[string][]string, error)
	// Online reports which users have at least one live connection on any gateway.
	Online(ctx context.Context, userIDs []string) (map[string]bool, error)
}

// GatewayOf extracts the gateway id from a registry member string.
func GatewayOf(member string) (string, bool) {
	for i := 0; i < len(member); i++ {
		if member[i] == '/' {
			return member[:i], i > 0
		}
	}
	return "", false
}
