package redis

import (
	"context"
	"encoding/json"
	"fmt"

	goredis "github.com/redis/go-redis/v9"

	"realtime-chat/backend/internal/platform/gwbus"
)

// Bus implements gwbus.Bus on Redis pub/sub: one channel per gateway plus one broadcast
// channel. Pub/sub is fire-and-forget: no persistence, no acknowledgement, no backpressure.
// That is acceptable only because clients repair any loss through sync (ADR-005, ADR-006).
//
// SECURITY: anything that can PUBLISH to these channels can inject frames to users' sockets.
// Redis must require authentication and be reachable only from the gateways (docs/security.md).
type Bus struct {
	client *goredis.Client
	prefix string
}

// NewBus returns a Bus; channels are named "<prefix>gw:<id>" and "<prefix>broadcast".
func NewBus(client *goredis.Client, prefix string) *Bus { return &Bus{client: client, prefix: prefix} }

func (b *Bus) gatewayChannel(id string) string { return b.prefix + "gw:" + id }
func (b *Bus) broadcastChannel() string        { return b.prefix + "broadcast" }

func (b *Bus) publish(ctx context.Context, channel string, m gwbus.Message) error {
	payload, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode bus message: %w", err)
	}
	if err := b.client.Publish(ctx, channel, payload).Err(); err != nil {
		return fmt.Errorf("bus publish: %w", err)
	}
	return nil
}

// Publish implements gwbus.Bus.
func (b *Bus) Publish(ctx context.Context, gatewayID string, m gwbus.Message) error {
	return b.publish(ctx, b.gatewayChannel(gatewayID), m)
}

// Broadcast implements gwbus.Bus.
func (b *Bus) Broadcast(ctx context.Context, m gwbus.Message) error {
	return b.publish(ctx, b.broadcastChannel(), m)
}

// Subscribe implements gwbus.Bus. It returns nil when ctx is done and an error if the initial
// subscription fails; after that go-redis reconnects and resubscribes on its own.
func (b *Bus) Subscribe(ctx context.Context, gatewayID string, h func(gwbus.Message)) error {
	ps := b.client.Subscribe(ctx, b.gatewayChannel(gatewayID), b.broadcastChannel())
	defer func() { _ = ps.Close() }()
	if _, err := ps.Receive(ctx); err != nil { // waits for the subscription confirmation
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("bus subscribe: %w", err)
	}
	ch := ps.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			var m gwbus.Message
			if err := json.Unmarshal([]byte(msg.Payload), &m); err != nil {
				continue // malformed payloads are ignored, never delivered
			}
			h(m)
		}
	}
}

var _ gwbus.Bus = (*Bus)(nil)
