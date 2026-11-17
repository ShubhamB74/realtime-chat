package ws

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"realtime-chat/backend/internal/app/messages"
	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/platform/httpx"
	"realtime-chat/backend/internal/transport/wire"
)

// Messages is what the gateway needs from the message service.
type Messages interface {
	Send(ctx context.Context, in messages.SendInput) (messages.SendOutput, error)
	Changes(ctx context.Context, userID, convID string, afterModSeq int64, limit int) (domain.Changes, error)
	Recipients(ctx context.Context, userID, convID string) ([]string, error)
}

// SessionChecker verifies that a session is still live (handshake and periodic re-check).
type SessionChecker interface {
	SessionActive(ctx context.Context, userID, familyID, deviceID string) (bool, error)
}

// Presence records which users have a live connection. It is best effort: failures are logged
// and never affect messaging.
type Presence interface {
	Touch(ctx context.Context, userID, connID string, ttl time.Duration) error
	Remove(ctx context.Context, userID, connID string) error
}

// TokenVerifier verifies access tokens.
type TokenVerifier interface {
	Verify(raw string) (auth.Identity, error)
}

// Config tunes the gateway. See DefaultConfig.
type Config struct {
	MaxConns           int
	MaxConnsPerUser    int
	SendQueue          int   // bounded outbound queue per connection (frames)
	MaxQueueBytes      int64 // ... and in bytes (a few large frames must not defeat the frame bound)
	PingInterval       time.Duration
	PongTimeout        time.Duration
	WriteTimeout       time.Duration
	RevalidateInterval time.Duration
	ReadLimit          int64 // max inbound frame size in bytes
	EventRate          float64
	EventBurst         int
	AllowedOrigins     []string // host patterns for browser clients, e.g. "localhost:5173"
}

// DefaultConfig returns conservative defaults (unmeasured; tune after load tests).
func DefaultConfig() Config {
	return Config{
		MaxConns: 5000, MaxConnsPerUser: 5, SendQueue: 256, MaxQueueBytes: 4 << 20,
		PingInterval: 25 * time.Second, PongTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second,
		RevalidateInterval: 60 * time.Second, ReadLimit: 32 << 10, EventRate: 20, EventBurst: 40,
	}
}

// Deps are the gateway's collaborators.
type Deps struct {
	Sessions SessionChecker
	Presence Presence
	Tokens   TokenVerifier
	Logger   *slog.Logger
}

// Stats is a snapshot of gateway counters.
type Stats struct {
	Connections   int
	Accepted      int64
	RejectedLimit int64
	SlowConsumers int64
	Published     int64
	Delivered     int64
}

var (
	errClosing   = errors.New("gateway is shutting down")
	errCapacity  = errors.New("gateway is at capacity")
	errUserLimit = errors.New("too many connections for this user")
)

// Hub owns all connections of this gateway process.
type Hub struct {
	cfg      Config
	deps     Deps
	log      *slog.Logger
	messages Messages

	mu      sync.RWMutex
	byID    map[string]*conn
	byUser  map[string]map[string]*conn
	total   int            // reserved + registered connections
	perUser map[string]int // same, per user
	closing bool
	wg      sync.WaitGroup

	accepted, rejected, slow, published, delivered atomic.Int64
	lastWarn                                       atomic.Int64 // unix nanos, throttles repeated warnings
}

// NewHub builds a gateway. Call Bind before serving.
func NewHub(cfg Config, d Deps) *Hub {
	return &Hub{cfg: cfg, deps: d, log: d.Logger, byID: map[string]*conn{}, byUser: map[string]map[string]*conn{}, perUser: map[string]int{}}
}

// Bind sets the message service (set after construction because the service publishes to the hub).
func (h *Hub) Bind(m Messages) { h.messages = m }

// Stats returns a snapshot of counters.
func (h *Hub) Stats() Stats {
	h.mu.RLock()
	n := len(h.byID)
	h.mu.RUnlock()
	return Stats{Connections: n, Accepted: h.accepted.Load(), RejectedLimit: h.rejected.Load(),
		SlowConsumers: h.slow.Load(), Published: h.published.Load(), Delivered: h.delivered.Load()}
}

// warnThrottled logs a warning at most once every 30 seconds, so a dependency outage does not
// produce one log line per connection per heartbeat.
func (h *Hub) warnThrottled(msg string, err error) {
	now := time.Now().UnixNano()
	last := h.lastWarn.Load()
	if now-last < int64(30*time.Second) || !h.lastWarn.CompareAndSwap(last, now) {
		return
	}
	h.log.Warn(msg, slog.String("error", err.Error()))
}

// Publish implements messages.Publisher: best-effort fan-out of a committed change.
func (h *Hub) Publish(ev messages.Event) {
	typ := EvReceived
	if ev.Type == messages.EventUpdated {
		typ = EvUpdated
	}
	b, err := newEvent(typ, "", wire.FromMessage(ev.Message))
	if err != nil {
		h.log.Error("encode event failed", slog.String("error", err.Error()))
		return
	}
	h.published.Add(1)
	h.deliver(ev.Recipients, b, ev.OriginConn)
}

// deliver enqueues b on every connection of the given users except exceptConn. It never blocks:
// a connection whose queue is full is closed as a slow consumer and repairs itself via sync.
func (h *Hub) deliver(userIDs []string, b []byte, exceptConn string) {
	h.mu.RLock()
	var targets []*conn
	for _, u := range userIDs {
		for _, c := range h.byUser[u] {
			if c.id != exceptConn {
				targets = append(targets, c)
			}
		}
	}
	h.mu.RUnlock()
	for _, c := range targets {
		if c.enqueue(b) {
			h.delivered.Add(1)
		}
	}
}

func (h *Hub) reserve(userID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.closing:
		return errClosing
	case h.total >= h.cfg.MaxConns:
		return errCapacity
	case h.perUser[userID] >= h.cfg.MaxConnsPerUser:
		return errUserLimit
	}
	h.total++
	h.perUser[userID]++
	return nil
}

func (h *Hub) release(userID string) {
	h.mu.Lock()
	h.total--
	if h.perUser[userID]--; h.perUser[userID] <= 0 {
		delete(h.perUser, userID)
	}
	h.mu.Unlock()
}

func (h *Hub) register(c *conn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closing {
		return false
	}
	h.byID[c.id] = c
	if h.byUser[c.ident.UserID] == nil {
		h.byUser[c.ident.UserID] = map[string]*conn{}
	}
	h.byUser[c.ident.UserID][c.id] = c
	h.wg.Add(1)
	return true
}

func (h *Hub) unregister(c *conn) {
	h.mu.Lock()
	delete(h.byID, c.id)
	if m := h.byUser[c.ident.UserID]; m != nil {
		delete(m, c.id)
		if len(m) == 0 {
			delete(h.byUser, c.ident.UserID)
		}
	}
	h.mu.Unlock()
	h.release(c.ident.UserID)
	h.wg.Done()
}

// Shutdown closes every connection with 1001 (going away) and waits for them to finish or for
// ctx to expire, after which remaining connections are dropped.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.closing = true
	conns := make([]*conn, 0, len(h.byID))
	for _, c := range h.byID {
		conns = append(conns, c)
	}
	h.mu.Unlock()

	for _, c := range conns {
		c.closeWith(websocket.StatusGoingAway, "server restarting")
	}
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		for _, c := range conns {
			_ = c.ws.CloseNow()
		}
		return ctx.Err()
	}
}

// tokenFromRequest reads the access token from the Authorization header or, for browsers, from
// a "bearer.<token>" entry in Sec-WebSocket-Protocol. Query-string tokens are deliberately not
// accepted: URLs end up in logs and browser history.
func tokenFromRequest(r *http.Request) string {
	if parts := strings.Fields(r.Header.Get("Authorization")); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); strings.HasPrefix(p, evBearerPrefix) {
				return strings.TrimPrefix(p, evBearerPrefix)
			}
		}
	}
	return ""
}

// ServeHTTP authenticates the handshake, enforces connection limits and upgrades the connection.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ident, err := h.deps.Tokens.Verify(tokenFromRequest(r))
	if err != nil {
		w.Header().Set("WWW-Authenticate", "Bearer")
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthenticated", "Authentication required", false)
		return
	}
	active, err := h.deps.Sessions.SessionActive(r.Context(), ident.UserID, ident.SessionID, ident.DeviceID)
	if err != nil {
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "Temporarily unavailable", true)
		return
	}
	if !active {
		w.Header().Set("WWW-Authenticate", "Bearer")
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthenticated", "Session is no longer active", false)
		return
	}
	if err := h.reserve(ident.UserID); err != nil {
		h.rejected.Add(1)
		status, code := http.StatusServiceUnavailable, "unavailable"
		if errors.Is(err, errUserLimit) {
			status, code = http.StatusTooManyRequests, "rate_limited"
		}
		w.Header().Set("Retry-After", "5")
		httpx.WriteError(w, r, status, code, err.Error(), true)
		return
	}

	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{Subprotocol},
		OriginPatterns:  h.cfg.AllowedOrigins,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		h.release(ident.UserID) // Accept has already written the HTTP error
		return
	}
	c := newConn(h, ident, wsConn)
	if !h.register(c) {
		h.release(ident.UserID)
		_ = wsConn.Close(websocket.StatusGoingAway, "server restarting")
		return
	}
	h.accepted.Add(1)
	c.serve()
}
