package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"realtime-chat/backend/internal/platform/gwbus"
)

// Presence tracks which users have a live WebSocket connection. It also serves as the
// connection registry (gwbus.Registry): members are "<gatewayID>/<connectionID>", so one
// structure answers both "is the user online?" and "which gateways hold their connections?". Each user has a sorted set of
// connection ids scored by expiry time (unix ms), so several devices and several gateway
// instances work without coordination. Redis is not authoritative: an outage degrades presence
// only, never messaging (ADR-006, ADR-007).
type Presence struct {
	client *goredis.Client
	prefix string
}

// NewPresence returns a Presence store.
func NewPresence(client *goredis.Client, prefix string) *Presence {
	return &Presence{client: client, prefix: prefix}
}

func (p *Presence) key(userID string) string { return p.prefix + userID }

// Touch marks a connection alive until now+ttl and refreshes the key's own expiry.
func (p *Presence) Touch(ctx context.Context, userID, connID string, ttl time.Duration) error {
	k := p.key(userID)
	exp := time.Now().Add(ttl).UnixMilli()
	pipe := p.client.TxPipeline()
	pipe.ZAdd(ctx, k, goredis.Z{Score: float64(exp), Member: connID})
	pipe.PExpire(ctx, k, 2*ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("presence touch: %w", err)
	}
	return nil
}

// Remove drops a connection immediately (clean disconnect).
func (p *Presence) Remove(ctx context.Context, userID, connID string) error {
	if err := p.client.ZRem(ctx, p.key(userID), connID).Err(); err != nil {
		return fmt.Errorf("presence remove: %w", err)
	}
	return nil
}

// Online reports which of the users have at least one unexpired connection. Expired entries
// are pruned as a side effect.
func (p *Presence) Online(ctx context.Context, userIDs []string) (map[string]bool, error) {
	now := fmt.Sprint(time.Now().UnixMilli())
	pipe := p.client.Pipeline()
	cards := make(map[string]*goredis.IntCmd, len(userIDs))
	for _, u := range userIDs {
		k := p.key(u)
		pipe.ZRemRangeByScore(ctx, k, "-inf", now)
		cards[u] = pipe.ZCard(ctx, k)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("presence online: %w", err)
	}
	out := make(map[string]bool, len(userIDs))
	for u, c := range cards {
		out[u] = c.Val() > 0
	}
	return out, nil
}

// Gateways maps gateway id -> users (among userIDs) that have an unexpired connection there.
func (p *Presence) Gateways(ctx context.Context, userIDs []string) (map[string][]string, error) {
	if len(userIDs) == 0 {
		return map[string][]string{}, nil
	}
	now := fmt.Sprint(time.Now().UnixMilli())
	pipe := p.client.Pipeline()
	cmds := make(map[string]*goredis.StringSliceCmd, len(userIDs))
	for _, u := range userIDs {
		cmds[u] = pipe.ZRangeByScore(ctx, p.key(u), &goredis.ZRangeBy{Min: now, Max: "+inf"})
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("presence gateways: %w", err)
	}
	out := map[string][]string{}
	for u, c := range cmds {
		seen := map[string]bool{}
		for _, member := range c.Val() {
			gw, ok := gwbus.GatewayOf(member)
			if ok && !seen[gw] {
				seen[gw] = true
				out[gw] = append(out[gw], u)
			}
		}
	}
	return out, nil
}

var _ gwbus.Registry = (*Presence)(nil)
