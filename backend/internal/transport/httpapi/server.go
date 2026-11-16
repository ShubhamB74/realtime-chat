// Package httpapi is the REST transport: routing, authentication, rate limiting and JSON.
// It contains no business rules; those live in internal/app.
package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"realtime-chat/backend/internal/app/accounts"
	"realtime-chat/backend/internal/app/chats"
	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/platform/httpx"
	"realtime-chat/backend/internal/platform/ratelimit"
)

// TokenVerifier verifies access tokens (implemented by auth.TokenIssuer).
type TokenVerifier interface {
	Verify(raw string) (auth.Identity, error)
}

// Deps are the collaborators of the REST API.
type Deps struct {
	Accounts *accounts.Service
	Chats    *chats.Service
	Tokens   TokenVerifier
	Limiter  ratelimit.Limiter // nil disables rate limiting (tests only)
	Logger   *slog.Logger
	// TrustProxy makes the client IP come from the right-most X-Forwarded-For entry. Enable only
	// when exactly one trusted reverse proxy appends that header.
	TrustProxy bool
}

// Server holds the handlers.
type Server struct{ Deps }

// New builds a Server.
func New(d Deps) *Server { return &Server{Deps: d} }

// Route is one registered endpoint.
type Route struct {
	Pattern string // "METHOD /path/{param}"
	Handler http.HandlerFunc
}

// Routes lists every endpoint. docs/api-specification.md and packages/openapi/openapi.yaml
// must describe each of them (checked by a contract test).
func (s *Server) Routes() []Route {
	return []Route{
		{"POST /v1/auth/register", s.register},
		{"POST /v1/auth/login", s.login},
		{"POST /v1/auth/refresh", s.refresh},
		{"POST /v1/auth/logout", s.authed(s.logout)},

		{"GET /v1/me", s.authed(s.getMe)},
		{"PATCH /v1/me", s.authed(s.updateMe)},
		{"GET /v1/users/lookup", s.authed(s.lookupUser)},

		{"GET /v1/devices", s.authed(s.listDevices)},
		{"DELETE /v1/devices/{id}", s.authed(s.revokeDevice)},
		{"GET /v1/sessions", s.authed(s.listSessions)},
		{"DELETE /v1/sessions/{id}", s.authed(s.revokeSession)},

		{"POST /v1/conversations/direct", s.authed(s.openDirect)},
		{"POST /v1/conversations/groups", s.authed(s.createGroup)},
		{"GET /v1/conversations", s.authed(s.listConversations)},
		{"GET /v1/conversations/{id}", s.authed(s.getConversation)},
		{"PATCH /v1/conversations/{id}", s.authed(s.renameConversation)},
		{"GET /v1/conversations/{id}/members", s.authed(s.listMembers)},
		{"POST /v1/conversations/{id}/members", s.authed(s.addMembers)},
		{"DELETE /v1/conversations/{id}/members/{userId}", s.authed(s.removeMember)},
		{"PUT /v1/conversations/{id}/members/{userId}/role", s.authed(s.setRole)},
		{"GET /v1/conversations/{id}/settings", s.authed(s.getSettings)},
		{"PATCH /v1/conversations/{id}/settings", s.authed(s.updateSettings)},
	}
}

// Register mounts all routes on mux.
func (s *Server) Register(mux *http.ServeMux) {
	for _, r := range s.Routes() {
		mux.HandleFunc(r.Pattern, r.Handler)
	}
}

// ---- authentication and rate limiting ------------------------------------------------------

type authedHandler func(w http.ResponseWriter, r *http.Request, id auth.Identity)

const generalUserLimit = 300 // requests per user per minute

func bearerToken(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}

// authed requires a valid access token and applies the per-user rate limit.
func (s *Server) authed(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := s.Tokens.Verify(bearerToken(r))
		if err != nil {
			s.fail(w, r, domain.Unauthenticated("Authentication required"))
			return
		}
		if !s.allow(w, r, "api:user:"+id.UserID, generalUserLimit, time.Minute) {
			return
		}
		h(w, r, id)
	}
}

// allow applies a rate limit and writes the 429 itself when exceeded. If the limiter backend
// fails it fails OPEN (logged): Redis is not a source of truth and an outage must not take
// authentication down (ADR-009).
func (s *Server) allow(w http.ResponseWriter, r *http.Request, key string, limit int, window time.Duration) bool {
	if s.Limiter == nil {
		return true
	}
	ok, retry, err := s.Limiter.Allow(r.Context(), key, limit, window)
	if err != nil {
		s.Logger.WarnContext(r.Context(), "rate limiter unavailable; failing open", slog.String("error", err.Error()))
		return true
	}
	if ok {
		return true
	}
	secs := int(math.Ceil(retry.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	httpx.WriteError(w, r, http.StatusTooManyRequests, "rate_limited", "Too many requests", true)
	return false
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			last := strings.TrimSpace(parts[len(parts)-1])
			if net.ParseIP(last) != nil {
				return last
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// emailKey returns a stable, non-reversible key fragment so rate-limit keys in Redis never
// contain email addresses.
func emailKey(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])[:32]
}

// ---- error mapping -------------------------------------------------------------------------

type tooLargeError struct{}

func (tooLargeError) Error() string { return "request body too large" }

// fail maps an error to the shared JSON error contract. Unknown errors become a generic 500;
// their details are logged, never returned.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	msg := ""
	var de *domain.Error
	if errors.As(err, &de) {
		msg = de.Msg
	}
	switch {
	case errors.Is(err, domain.ErrInvalid):
		httpx.WriteError(w, r, http.StatusBadRequest, "invalid_request", msg, false)
	case errors.Is(err, domain.ErrUnauthenticated):
		w.Header().Set("WWW-Authenticate", "Bearer")
		httpx.WriteError(w, r, http.StatusUnauthorized, "unauthenticated", msg, false)
	case errors.Is(err, domain.ErrForbidden):
		httpx.WriteError(w, r, http.StatusForbidden, "forbidden", msg, false)
	case errors.Is(err, domain.ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, "not_found", "Resource not found", false)
	case errors.Is(err, domain.ErrConflict):
		httpx.WriteError(w, r, http.StatusConflict, "conflict", msg, false)
	case errors.As(err, new(tooLargeError)):
		httpx.WriteError(w, r, http.StatusRequestEntityTooLarge, "payload_too_large", "Request body is too large", false)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		httpx.WriteError(w, r, http.StatusServiceUnavailable, "unavailable", "Request was interrupted", true)
	default:
		s.Logger.ErrorContext(r.Context(), "unhandled error",
			slog.String("request_id", httpx.RequestIDFrom(r.Context())),
			slog.String("error", err.Error()))
		httpx.WriteError(w, r, http.StatusInternalServerError, "internal", "Internal server error", false)
	}
}
