package ws

import (
	"context"
	"math/rand/v2"
	"time"

	"realtime-chat/backend/internal/platform/gwbus"
	"realtime-chat/backend/internal/platform/retry"
)

// This file implements cross-gateway delivery and presence announcements (ADR-006).
//
// Flow for a committed message: the service calls Hub.Publish on the gateway that handled the
// request. That gateway delivers to its own connections immediately, then queues a "remote"
// task. A worker looks up, in the registry, which other gateways hold connections of the
// recipients and publishes one bus message per gateway. Everything after the local delivery is
// best effort and asynchronous: if the registry or bus is down, or the queue is full, live
// delivery to other gateways is skipped and those clients converge through sync (ADR-005).

// Start launches the background workers (bus subscription, cross-gateway senders, cursor
// flusher). It is safe to call once; Shutdown stops them.
func (h *Hub) Start() {
	if !h.started.CompareAndSwap(false, true) {
		return
	}
	if h.deps.Bus != nil && h.deps.Presence != nil {
		h.bgWG.Add(1 + remoteWorkers)
		go h.busLoop()
		for i := 0; i < remoteWorkers; i++ {
			go h.remoteWorker()
		}
	}
	if h.receipts != nil {
		h.bgWG.Add(1)
		go h.flushLoop()
	}
}

// fanout delivers a frame to the users' connections on this gateway and queues delivery to the
// other gateways.
func (h *Hub) fanout(userIDs []string, frame []byte, except string) {
	h.deliver(userIDs, frame, except)
	h.enqueueRemote(remoteTask{users: userIDs, frame: frame, except: except})
}

func (h *Hub) enqueueRemote(t remoteTask) {
	if h.deps.Bus == nil || h.deps.Presence == nil || !h.started.Load() || len(t.users) == 0 {
		return
	}
	select {
	case h.remoteQ <- t:
	default:
		h.remoteDropped.Add(1) // sync repairs it
	}
}

func (h *Hub) remoteWorker() {
	defer h.bgWG.Done()
	for {
		select {
		case <-h.bgCtx.Done():
			return
		case t := <-h.remoteQ:
			h.sendRemote(t)
		}
	}
}

func (h *Hub) sendRemote(t remoteTask) {
	ctx, cancel := context.WithTimeout(h.bgCtx, 2*time.Second)
	defer cancel()
	byGateway, err := h.deps.Presence.Gateways(ctx, t.users)
	if err != nil {
		h.remoteFailed.Add(1)
		h.warnThrottled("cross-gateway lookup failed; live delivery to other gateways falls back to sync", err)
		return
	}
	for gw, users := range byGateway {
		if gw == h.gatewayID {
			continue // already delivered locally
		}
		err := h.deps.Bus.Publish(ctx, gw, gwbus.Message{Kind: gwbus.KindDeliver, Users: users, Except: t.except, Frame: t.frame})
		if err != nil {
			h.remoteFailed.Add(1)
			h.warnThrottled("cross-gateway publish failed; live delivery falls back to sync", err)
			continue
		}
		h.remotePublished.Add(1)
	}
}

// busLoop keeps a subscription to this gateway's channel and the broadcast channel alive,
// retrying with exponential backoff and jitter.
func (h *Hub) busLoop() {
	defer h.bgWG.Done()
	attempt := 0
	for h.bgCtx.Err() == nil {
		began := time.Now()
		err := h.deps.Bus.Subscribe(h.bgCtx, h.gatewayID, h.onBusMessage)
		if h.bgCtx.Err() != nil {
			return
		}
		if err != nil {
			h.warnThrottled("gateway bus subscription failed; retrying", err)
		}
		if time.Since(began) > 30*time.Second {
			attempt = 0 // it was healthy for a while: start the backoff over
		}
		wait := retry.Backoff(min(attempt, 6), 200*time.Millisecond, 5*time.Second, rand.Float64) //nolint:gosec // jitter
		attempt++
		select {
		case <-h.bgCtx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (h *Hub) onBusMessage(m gwbus.Message) {
	switch m.Kind {
	case gwbus.KindDeliver:
		h.remoteReceived.Add(1)
		h.deliver(m.Users, m.Frame, m.Except)
	case gwbus.KindPresence:
		h.onPresenceChange(m.User, m.Online)
	}
}

// ---- presence ---------------------------------------------------------------------------------

// announcePresence tells every gateway (including this one) that a user went online or offline.
// Without a bus (standalone gateway) it is handled locally.
func (h *Hub) announcePresence(userID string, online bool) {
	if h.deps.Bus == nil {
		h.onPresenceChange(userID, online)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := h.deps.Bus.Broadcast(ctx, gwbus.Message{Kind: gwbus.KindPresence, User: userID, Online: online}); err != nil {
		h.warnThrottled("presence broadcast failed", err)
	}
}

// onPresenceChange pushes presence.changed to this gateway's subscribers of the user.
func (h *Hub) onPresenceChange(userID string, online bool) {
	frame, err := newEvent(EvPresenceChanged, "", presenceChanged{UserID: userID, Online: online})
	if err != nil {
		return
	}
	h.mu.RLock()
	targets := make([]*conn, 0, len(h.subs[userID]))
	for _, c := range h.subs[userID] {
		targets = append(targets, c)
	}
	h.mu.RUnlock()
	for _, c := range targets {
		c.enqueue(frame)
	}
}

// scheduleOfflineCheck announces "offline" after a grace period unless the user came back (or
// is connected through another gateway). The delay absorbs reconnects and page reloads.
func (h *Hub) scheduleOfflineCheck(userID string) {
	if h.deps.Presence == nil {
		return
	}
	time.AfterFunc(h.cfg.PresenceGrace, func() {
		h.mu.RLock()
		closing := h.closing
		h.mu.RUnlock()
		if closing {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		on, err := h.deps.Presence.Online(ctx, []string{userID})
		if err != nil {
			h.warnThrottled("presence lookup failed", err)
			return
		}
		if !on[userID] {
			h.announcePresence(userID, false)
		}
	})
}

// subscribe records that c watches the users' presence. It reports false (and changes nothing)
// if that would exceed the per-connection cap.
func (h *Hub) subscribe(c *conn, users []string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	added := 0
	for _, u := range users {
		if _, has := c.watching[u]; !has {
			added++
		}
	}
	if len(c.watching)+added > h.cfg.MaxPresenceSubs {
		return false
	}
	for _, u := range users {
		if _, has := c.watching[u]; has {
			continue
		}
		c.watching[u] = struct{}{}
		if h.subs[u] == nil {
			h.subs[u] = map[string]*conn{}
		}
		h.subs[u][c.id] = c
	}
	return true
}

func (h *Hub) unsubscribe(c *conn, users []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, u := range users {
		delete(c.watching, u)
		if m := h.subs[u]; m != nil {
			delete(m, c.id)
			if len(m) == 0 {
				delete(h.subs, u)
			}
		}
	}
}
