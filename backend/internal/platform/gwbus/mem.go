package gwbus

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ErrInjected is returned when a Mem has been told to fail.
var ErrInjected = errors.New("gwbus: injected failure")

// Mem is an in-process Bus and Registry with the same observable semantics as the Redis
// implementations (asynchronous delivery, broadcast reaches the sender, expiring entries). It
// exists for tests and for single-process development without Redis; it can inject failures to
// model a Redis outage.
type Mem struct {
	mu       sync.Mutex
	subs     map[string][]*memSub // gateway id -> subscribers
	conns    map[string]map[string]time.Time
	failing  atomic.Bool
	buffered int
}

type memSub struct {
	gateway string
	ch      chan Message
}

// NewMem returns an empty Mem.
func NewMem() *Mem {
	return &Mem{subs: map[string][]*memSub{}, conns: map[string]map[string]time.Time{}, buffered: 4096}
}

// SetFailing makes every Bus and Registry call return ErrInjected (true) or work again (false).
func (m *Mem) SetFailing(v bool) { m.failing.Store(v) }

func (m *Mem) check() error {
	if m.failing.Load() {
		return ErrInjected
	}
	return nil
}

// Subscribers returns how many subscriptions are active for a gateway (tests use it to wait
// until a gateway is ready to receive).
func (m *Mem) Subscribers(gatewayID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.subs[gatewayID])
}

// Publish implements Bus.
func (m *Mem) Publish(_ context.Context, gatewayID string, msg Message) error {
	if err := m.check(); err != nil {
		return err
	}
	m.mu.Lock()
	targets := append([]*memSub(nil), m.subs[gatewayID]...)
	m.mu.Unlock()
	for _, s := range targets {
		select {
		case s.ch <- msg:
		default: // pub/sub drops when a subscriber cannot keep up
		}
	}
	return nil
}

// Broadcast implements Bus.
func (m *Mem) Broadcast(_ context.Context, msg Message) error {
	if err := m.check(); err != nil {
		return err
	}
	m.mu.Lock()
	var targets []*memSub
	for _, list := range m.subs {
		targets = append(targets, list...)
	}
	m.mu.Unlock()
	for _, s := range targets {
		select {
		case s.ch <- msg:
		default:
		}
	}
	return nil
}

// Subscribe implements Bus.
func (m *Mem) Subscribe(ctx context.Context, gatewayID string, h func(Message)) error {
	if err := m.check(); err != nil {
		return err
	}
	s := &memSub{gateway: gatewayID, ch: make(chan Message, m.buffered)}
	m.mu.Lock()
	m.subs[gatewayID] = append(m.subs[gatewayID], s)
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		list := m.subs[gatewayID]
		for i, x := range list {
			if x == s {
				m.subs[gatewayID] = append(list[:i], list[i+1:]...)
				break
			}
		}
		m.mu.Unlock()
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-s.ch:
			h(msg)
		}
	}
}

// Touch implements Registry.
func (m *Mem) Touch(_ context.Context, userID, member string, ttl time.Duration) error {
	if err := m.check(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conns[userID] == nil {
		m.conns[userID] = map[string]time.Time{}
	}
	m.conns[userID][member] = time.Now().Add(ttl)
	return nil
}

// Remove implements Registry.
func (m *Mem) Remove(_ context.Context, userID, member string) error {
	if err := m.check(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conns[userID], member)
	return nil
}

// Gateways implements Registry.
func (m *Mem) Gateways(_ context.Context, userIDs []string) (map[string][]string, error) {
	if err := m.check(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := map[string][]string{}
	for _, u := range userIDs {
		seen := map[string]bool{}
		for member, exp := range m.conns[u] {
			gw, ok := GatewayOf(member)
			if ok && exp.After(now) && !seen[gw] {
				seen[gw] = true
				out[gw] = append(out[gw], u)
			}
		}
	}
	return out, nil
}

// Online implements Registry.
func (m *Mem) Online(_ context.Context, userIDs []string) (map[string]bool, error) {
	if err := m.check(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	out := make(map[string]bool, len(userIDs))
	for _, u := range userIDs {
		for _, exp := range m.conns[u] {
			if exp.After(now) {
				out[u] = true
				break
			}
		}
	}
	return out, nil
}

var (
	_ Bus      = (*Mem)(nil)
	_ Registry = (*Mem)(nil)
)
