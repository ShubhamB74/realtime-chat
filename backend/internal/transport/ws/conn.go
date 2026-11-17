package ws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"realtime-chat/backend/internal/app/messages"
	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/transport/wire"
)

const (
	syncMaxConversations = 20
	syncPageSize         = 50
	syncMinGap           = time.Second
	handlerTimeout       = 10 * time.Second
	typingStartGap       = 2 * time.Second
	typingStopGap        = 500 * time.Millisecond
	maxTypingEntries     = 100
	abuseWindow          = 10 * time.Second
	abuseDeniedLimit     = 200
)

// conn is one client connection. All outbound frames go through the bounded send queue and a
// single writer goroutine, so a slow client can never hold more than SendQueue frames in memory.
type conn struct {
	hub    *Hub
	id     string
	ident  auth.Identity
	ws     *websocket.Conn
	send   chan []byte
	ctx    context.Context
	cancel context.CancelFunc

	closeOnce   sync.Once
	closed      atomic.Bool
	queuedBytes atomic.Int64 // bytes currently waiting in send

	// Touched only by the read goroutine.
	bucket      *bucket
	deniedCount int
	deniedSince time.Time
	typingAt    map[string]time.Time
	lastSync    time.Time
}

func newConn(h *Hub, ident auth.Identity, ws *websocket.Conn) *conn {
	ctx, cancel := context.WithCancel(context.Background())
	return &conn{
		hub: h, id: newID(), ident: ident, ws: ws,
		send: make(chan []byte, h.cfg.SendQueue), ctx: ctx, cancel: cancel,
		bucket: newBucket(h.cfg.EventRate, h.cfg.EventBurst, nil), typingAt: map[string]time.Time{},
	}
}

// enqueue adds a frame to the outbound queue without blocking. If the queue is full the client
// is too slow: it is disconnected (it recovers through sync) rather than buffered without limit.
func (c *conn) enqueue(b []byte) bool {
	if c.closed.Load() {
		return false
	}
	n := int64(len(b))
	if limit := c.hub.cfg.MaxQueueBytes; limit > 0 && c.queuedBytes.Add(n) > limit {
		c.queuedBytes.Add(-n)
		c.hub.slow.Add(1)
		c.closeWith(CloseSlowConsumer, "slow consumer")
		return false
	}
	select {
	case c.send <- b:
		return true
	default:
		c.queuedBytes.Add(-n)
		c.hub.slow.Add(1)
		c.closeWith(CloseSlowConsumer, "slow consumer")
		return false
	}
}

// closeWith starts the close handshake exactly once. It does not cancel the connection context:
// the read loop ends by itself when the handshake completes, which lets the client receive our
// close code.
func (c *conn) closeWith(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		go func() { _ = c.ws.Close(code, reason) }()
	})
}

func (c *conn) logger() *slog.Logger {
	return c.hub.log.With(slog.String("conn_id", c.id), slog.String("user_id", c.ident.UserID))
}

func (c *conn) serve() {
	defer c.hub.unregister(c)
	c.ws.SetReadLimit(c.hub.cfg.ReadLimit)

	if p := c.hub.deps.Presence; p != nil {
		if err := p.Touch(c.ctx, c.ident.UserID, c.id, 3*c.hub.cfg.PingInterval); err != nil {
			c.hub.warnThrottled("presence unavailable", err)
		}
	}
	c.sendEvent(EvReady, "", readyPayload{
		ConnectionID: c.id, UserID: c.ident.UserID, DeviceID: c.ident.DeviceID,
		HeartbeatIntervalMS: c.hub.cfg.PingInterval.Milliseconds(), MaxFrameBytes: c.hub.cfg.ReadLimit,
		ServerTime: time.Now().UTC().Format(time.RFC3339Nano),
	})

	var wg sync.WaitGroup
	for _, loop := range []func(){c.writeLoop, c.pingLoop, c.revalidateLoop} {
		wg.Add(1)
		go func() { defer wg.Done(); c.guard(loop) }()
	}
	c.readLoop()

	c.cancel()
	wg.Wait()
	_ = c.ws.CloseNow()
	if p := c.hub.deps.Presence; p != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := p.Remove(ctx, c.ident.UserID, c.id); err != nil {
			c.hub.warnThrottled("presence unavailable", err)
		}
		cancel()
	}
	c.logger().Debug("connection closed")
}

// guard converts a panic in a connection goroutine into a closed connection instead of a
// crashed process. Only the panic type is logged (values may contain user data).
func (c *conn) guard(fn func()) {
	defer func() {
		if rec := recover(); rec != nil {
			c.logger().Error("panic in connection goroutine", slog.Any("panic_type", typeName(rec)))
			c.closeWith(websocket.StatusInternalError, "internal error")
		}
	}()
	fn()
}

func typeName(v any) string {
	switch v.(type) {
	case error:
		return "error"
	case string:
		return "string"
	default:
		return "other"
	}
}

func (c *conn) readLoop() {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			c.logger().Debug("read ended", slog.Int("close_status", int(websocket.CloseStatus(err))))
			return
		}
		if typ != websocket.MessageText {
			c.closeWith(websocket.StatusUnsupportedData, "text frames only")
			continue
		}
		c.guard(func() { c.handle(data) })
	}
}

func (c *conn) writeLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case b := <-c.send:
			ctx, cancel := context.WithTimeout(c.ctx, c.hub.cfg.WriteTimeout)
			err := c.ws.Write(ctx, websocket.MessageText, b)
			cancel()
			c.queuedBytes.Add(-int64(len(b)))
			if err != nil {
				c.closeWith(websocket.StatusGoingAway, "write failed")
				return
			}
		}
	}
}

func (c *conn) pingLoop() {
	t := time.NewTicker(c.hub.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(c.ctx, c.hub.cfg.PongTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				if c.ctx.Err() == nil {
					c.closeWith(websocket.StatusGoingAway, "heartbeat timeout")
				}
				return
			}
			if p := c.hub.deps.Presence; p != nil {
				if err := p.Touch(c.ctx, c.ident.UserID, c.id, 3*c.hub.cfg.PingInterval); err != nil {
					c.hub.warnThrottled("presence unavailable", err)
				}
			}
		}
	}
}

// revalidateLoop closes the connection soon after its session is revoked, which the stateless
// access token alone could not do (ADR-009).
func (c *conn) revalidateLoop() {
	t := time.NewTicker(c.hub.cfg.RevalidateInterval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
			ok, err := c.hub.deps.Sessions.SessionActive(ctx, c.ident.UserID, c.ident.SessionID, c.ident.DeviceID)
			cancel()
			if err != nil {
				c.hub.warnThrottled("session revalidation failed", err)
				continue
			}
			if !ok {
				c.closeWith(CloseSessionRevoked, "session revoked")
				return
			}
		}
	}
}

// ---- outbound helpers -------------------------------------------------------------------------

func (c *conn) sendEvent(typ, requestID string, payload any) {
	b, err := newEvent(typ, requestID, payload)
	if err != nil {
		c.logger().Error("encode event failed", slog.String("type", typ))
		return
	}
	c.enqueue(b)
}

func (c *conn) sendError(requestID, code, msg string, retryable bool) {
	c.sendEvent(EvError, requestID, errorPayload{Code: code, Message: msg, Retryable: retryable})
}

func (c *conn) sendDomainError(requestID string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		c.sendError(requestID, "unavailable", "Request timed out", true)
		return
	}
	code, msg, retry := classify(err)
	if code == "internal" {
		c.logger().Error("event handler failed", slog.String("error", err.Error()))
	}
	c.sendError(requestID, code, msg, retry)
}

// ---- inbound handling -------------------------------------------------------------------------

func decodeStrict(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		return domain.Invalid("payload is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return domain.Invalid("payload is not valid for this event")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return domain.Invalid("payload is not valid for this event")
	}
	return nil
}

// rateLimited applies the per-connection token bucket and closes persistent abusers.
func (c *conn) rateLimited(requestID string) bool {
	if c.bucket.allow() {
		return false
	}
	now := time.Now()
	if now.Sub(c.deniedSince) > abuseWindow {
		c.deniedSince, c.deniedCount = now, 0
	}
	c.deniedCount++
	if c.deniedCount > abuseDeniedLimit {
		c.closeWith(websocket.StatusPolicyViolation, "rate limit exceeded")
		return true
	}
	c.sendError(requestID, "rate_limited", "Too many events; slow down", true)
	return true
}

func (c *conn) handle(data []byte) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		if c.rateLimited("") {
			return
		}
		c.sendError("", "invalid_request", "Frame is not a valid JSON envelope", false)
		return
	}
	if c.rateLimited(env.RequestID) {
		return
	}
	if env.V != ProtocolVersion {
		c.sendError(env.RequestID, "unsupported_version", "Only protocol version 1 is supported", false)
		return
	}
	switch env.Type {
	case EvMessageSend:
		c.onSend(env)
	case EvSyncRequest:
		c.onSync(env)
	case EvTypingStart:
		c.onTyping(env, true)
	case EvTypingStop:
		c.onTyping(env, false)
	default:
		if when, planned := plannedEvents[env.Type]; planned {
			c.sendError(env.RequestID, "unsupported_event", "Not available yet: "+when, false)
			return
		}
		c.sendError(env.RequestID, "unsupported_event", "Unknown event type", false)
	}
}

func (c *conn) onSend(env Envelope) {
	var p sendPayload
	if err := decodeStrict(env.Payload, &p); err != nil {
		c.sendDomainError(env.RequestID, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, handlerTimeout)
	defer cancel()
	out, err := c.hub.messages.Send(ctx, messages.SendInput{
		UserID: c.ident.UserID, DeviceID: c.ident.DeviceID, ConversationID: p.ConversationID,
		IdempotencyKey: p.IdempotencyKey, Body: p.Body, OriginConn: c.id,
	})
	if err != nil {
		c.sendDomainError(env.RequestID, err)
		return
	}
	// Acceptance is acknowledged only after the message is durably stored (ADR-003).
	c.sendEvent(EvAccepted, env.RequestID, acceptedPayload{Message: wire.FromMessage(out.Message), Duplicate: out.Duplicate})
}

func (c *conn) onSync(env Envelope) {
	// A sync request fans out to up to 20 database queries and up to 1000 messages, so it is
	// limited separately from the general event rate.
	now := time.Now()
	if now.Sub(c.lastSync) < syncMinGap {
		c.sendError(env.RequestID, "rate_limited", "Sync requests are limited to one per second", true)
		return
	}
	c.lastSync = now
	var p syncPayload
	if err := decodeStrict(env.Payload, &p); err != nil {
		c.sendDomainError(env.RequestID, err)
		return
	}
	if n := len(p.Conversations); n == 0 || n > syncMaxConversations {
		c.sendError(env.RequestID, "invalid_request", "Provide between 1 and 20 conversations to sync", false)
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, handlerTimeout)
	defer cancel()
	for i, item := range p.Conversations {
		batch := syncBatch{ConversationID: item.ConversationID, Done: i == len(p.Conversations)-1, Changes: []wire.Message{}}
		ch, err := c.hub.messages.Changes(ctx, c.ident.UserID, item.ConversationID, item.AfterModSeq, syncPageSize)
		switch {
		case errors.Is(err, domain.ErrNotFound):
			batch.Status = "not_found"
		case err != nil:
			c.sendDomainError(env.RequestID, err)
			return
		default:
			batch.Status, batch.Changes, batch.HasMore, batch.LastSeq = "ok", wire.FromMessages(ch.Messages), ch.HasMore, ch.LastSeq
		}
		c.sendEvent(EvSyncBatch, env.RequestID, batch)
	}
}

func (c *conn) onTyping(env Envelope, start bool) {
	var p typingPayload
	if err := decodeStrict(env.Payload, &p); err != nil {
		c.sendDomainError(env.RequestID, err)
		return
	}
	gap := typingStopGap
	if start {
		gap = typingStartGap
	}
	now := time.Now()
	if last, ok := c.typingAt[p.ConversationID]; ok && now.Sub(last) < gap {
		return // throttled silently: typing indicators are ephemeral
	}
	if len(c.typingAt) >= maxTypingEntries {
		c.typingAt = map[string]time.Time{}
	}
	c.typingAt[p.ConversationID] = now

	ctx, cancel := context.WithTimeout(c.ctx, handlerTimeout)
	defer cancel()
	members, err := c.hub.messages.Recipients(ctx, c.ident.UserID, p.ConversationID)
	if err != nil {
		c.sendDomainError(env.RequestID, err)
		return
	}
	others := make([]string, 0, len(members))
	for _, m := range members {
		if m != c.ident.UserID {
			others = append(others, m)
		}
	}
	b, err := newEvent(EvTyping, "", typingUpdate{ConversationID: p.ConversationID, UserID: c.ident.UserID, Typing: start})
	if err != nil {
		return
	}
	c.hub.deliver(others, b, "")
}
