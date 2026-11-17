package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"realtime-chat/backend/internal/auth"
	"realtime-chat/backend/internal/domain"
	"realtime-chat/backend/internal/platform/httpx"
	"realtime-chat/backend/internal/platform/ratelimit"
)

type fakeVerifier struct{ id auth.Identity }

func (f fakeVerifier) Verify(raw string) (auth.Identity, error) {
	if raw == "good" {
		return f.id, nil
	}
	return auth.Identity{}, auth.ErrInvalidToken
}

type failingLimiter struct{}

func (failingLimiter) Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error) {
	return false, 0, errors.New("redis down")
}

func testServer(l ratelimit.Limiter) *Server {
	return New(Deps{
		Tokens:  fakeVerifier{id: auth.Identity{UserID: "user-1", SessionID: "sess-1", DeviceID: "dev-1"}},
		Limiter: l,
		Logger:  slog.New(slog.NewJSONHandler(io.Discard, nil)),
	})
}

func serve(s *Server, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	httpx.Chain(h, httpx.RequestID).ServeHTTP(rec, req)
	return rec
}

func errBody(t *testing.T, rec *httptest.ResponseRecorder) httpx.ErrorBody {
	t.Helper()
	var b httpx.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("bad error body %q: %v", rec.Body.String(), err)
	}
	return b
}

// ---- authentication ------------------------------------------------------------------------

func TestAuthedRejectsMissingMalformedAndInvalidTokens(t *testing.T) {
	s := testServer(nil)
	called := false
	h := s.authed(func(http.ResponseWriter, *http.Request, auth.Identity) { called = true })

	for name, header := range map[string]string{
		"missing": "", "wrong scheme": "Basic good", "no token": "Bearer", "bad token": "Bearer nope", "extra parts": "Bearer good extra",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			rec := serve(s, h, req)
			if rec.Code != http.StatusUnauthorized || errBody(t, rec).Code != "unauthenticated" {
				t.Fatalf("code=%d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Error("missing WWW-Authenticate header")
			}
		})
	}
	if called {
		t.Fatal("handler must not run without valid credentials")
	}
}

func TestAuthedPassesIdentity(t *testing.T) {
	s := testServer(nil)
	var got auth.Identity
	h := s.authed(func(w http.ResponseWriter, _ *http.Request, id auth.Identity) {
		got = id
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "bearer good") // scheme is case-insensitive
	rec := serve(s, h, req)
	if rec.Code != http.StatusNoContent || got.UserID != "user-1" || got.SessionID != "sess-1" {
		t.Fatalf("code=%d identity=%+v", rec.Code, got)
	}
}

// ---- rate limiting -------------------------------------------------------------------------

func TestRateLimitReturns429WithRetryAfter(t *testing.T) {
	s := testServer(ratelimit.NewMemory(nil))
	h := s.authed(func(w http.ResponseWriter, _ *http.Request, _ auth.Identity) { w.WriteHeader(http.StatusNoContent) })
	var last *httptest.ResponseRecorder
	for i := 0; i < generalUserLimit+1; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
		req.Header.Set("Authorization", "Bearer good")
		last = serve(s, h, req)
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("code = %d", last.Code)
	}
	b := errBody(t, last)
	if b.Code != "rate_limited" || !b.Retryable || last.Header().Get("Retry-After") == "" {
		t.Errorf("body=%+v retry-after=%q", b, last.Header().Get("Retry-After"))
	}
}

func TestRateLimiterFailureFailsOpen(t *testing.T) {
	s := testServer(failingLimiter{})
	h := s.authed(func(w http.ResponseWriter, _ *http.Request, _ auth.Identity) { w.WriteHeader(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Authorization", "Bearer good")
	if rec := serve(s, h, req); rec.Code != http.StatusNoContent {
		t.Fatalf("limiter outage must not block requests, got %d", rec.Code)
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.5:4242"
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 203.0.113.9")
	if got := clientIP(req, false); got != "10.0.0.5" {
		t.Errorf("untrusted proxy: got %q", got)
	}
	if got := clientIP(req, true); got != "203.0.113.9" {
		t.Errorf("trusted proxy must use the right-most entry, got %q", got)
	}
	req.Header.Set("X-Forwarded-For", "not-an-ip")
	if got := clientIP(req, true); got != "10.0.0.5" {
		t.Errorf("garbage header must fall back to the socket address, got %q", got)
	}
}

func TestEmailKeyIsStableAndHidesEmail(t *testing.T) {
	a, b := emailKey(" Alice@Example.com "), emailKey("alice@example.com")
	if a != b || len(a) != 32 || strings.Contains(a, "alice") {
		t.Errorf("a=%q b=%q", a, b)
	}
}

// ---- JSON decoding and errors --------------------------------------------------------------

func decode(t *testing.T, body string) error {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	var dst struct {
		A string `json:"a"`
	}
	return decodeJSON(httptest.NewRecorder(), req, &dst)
}

func TestDecodeJSONStrictness(t *testing.T) {
	if err := decode(t, `{"a":"x"}`); err != nil {
		t.Errorf("valid body rejected: %v", err)
	}
	for name, body := range map[string]string{
		"unknown field": `{"a":"x","b":1}`, "trailing data": `{"a":"x"} {"a":"y"}`,
		"not json": `nope`, "wrong type": `{"a":5}`, "empty": ``,
	} {
		if err := decode(t, body); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%s: want ErrInvalid, got %v", name, err)
		}
	}
	if err := decode(t, `{"a":"`+strings.Repeat("x", maxBodyBytes)+`"}`); !errors.As(err, new(tooLargeError)) {
		t.Errorf("oversized body: want tooLargeError, got %v", err)
	}
}

func TestFailMapsErrorKinds(t *testing.T) {
	s := testServer(nil)
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.Invalid("bad"), 400, "invalid_request"},
		{domain.Unauthenticated("no"), 401, "unauthenticated"},
		{domain.Forbidden("nope"), 403, "forbidden"},
		{domain.NotFound(), 404, "not_found"},
		{domain.Conflict("dup"), 409, "conflict"},
		{tooLargeError{}, 413, "payload_too_large"},
		{context.Canceled, 503, "unavailable"},
		{errors.New("pq: password authentication failed for user secret"), 500, "internal"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		s.fail(rec, httptest.NewRequest(http.MethodGet, "/", nil), c.err)
		b := errBody(t, rec)
		if rec.Code != c.status || b.Code != c.code {
			t.Errorf("%v: got %d/%s, want %d/%s", c.err, rec.Code, b.Code, c.status, c.code)
		}
		if c.status == 500 && strings.Contains(rec.Body.String(), "secret") {
			t.Error("internal error details leaked to the client")
		}
	}
}

func TestNotFoundNeverEchoesDetails(t *testing.T) {
	s := testServer(nil)
	rec := httptest.NewRecorder()
	s.fail(rec, httptest.NewRequest(http.MethodGet, "/", nil), &domain.Error{Kind: domain.ErrNotFound, Msg: "internal detail"})
	if strings.Contains(rec.Body.String(), "internal detail") {
		t.Error("not_found must use the generic message")
	}
}

// ---- cursor --------------------------------------------------------------------------------

func TestCursorRoundTripAndRejection(t *testing.T) {
	in := &domain.ListCursor{UpdatedAt: time.Date(2026, 11, 13, 10, 0, 0, 123456000, time.UTC), ID: "11111111-1111-1111-1111-111111111111"}
	enc := encodeCursor(in)
	if enc == nil {
		t.Fatal("nil cursor")
	}
	out, err := decodeCursor(*enc)
	if err != nil || !out.UpdatedAt.Equal(in.UpdatedAt) || out.ID != in.ID {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if c, err := decodeCursor(""); c != nil || err != nil {
		t.Errorf("empty cursor: %v %v", c, err)
	}
	for _, bad := range []string{"!!!", "e30", "bm90LWpzb24"} {
		if _, err := decodeCursor(bad); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestParseLimit(t *testing.T) {
	if n, err := parseLimit(""); n != 0 || err != nil {
		t.Errorf("empty: %d %v", n, err)
	}
	if n, err := parseLimit("25"); n != 25 || err != nil {
		t.Errorf("25: %d %v", n, err)
	}
	for _, bad := range []string{"0", "-1", "abc"} {
		if _, err := parseLimit(bad); !errors.Is(err, domain.ErrInvalid) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// ---- contract: every route is documented in the OpenAPI file --------------------------------

func TestEveryRouteIsInOpenAPI(t *testing.T) {
	raw, err := os.ReadFile("../../../../packages/openapi/openapi.yaml")
	if err != nil {
		t.Fatalf("read openapi: %v", err)
	}
	spec := string(raw)
	srv := testServer(nil)
	srv.WS = http.NotFoundHandler() // include the WebSocket route in the contract check
	for _, r := range srv.Routes() {
		method, path, ok := strings.Cut(r.Pattern, " ")
		if !ok {
			t.Fatalf("bad pattern %q", r.Pattern)
		}
		pathLine := "\n  " + path + ":"
		i := strings.Index(spec, pathLine)
		if i < 0 {
			t.Errorf("%s: path %s not documented", r.Pattern, path)
			continue
		}
		// The method key must appear before the next path entry.
		rest := spec[i+len(pathLine):]
		if j := strings.Index(rest, "\n  /"); j >= 0 {
			rest = rest[:j]
		}
		if !strings.Contains(rest, "\n    "+strings.ToLower(method)+":") {
			t.Errorf("%s: method not documented", r.Pattern)
		}
	}
}
