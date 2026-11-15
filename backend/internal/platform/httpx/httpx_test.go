package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, RequestIDFrom(r.Context()))
	})
}

func TestRequestIDGeneratedWhenAbsent(t *testing.T) {
	rec := httptest.NewRecorder()
	RequestID(okHandler()).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	id := rec.Header().Get(RequestIDHeader)
	if !validRequestID.MatchString(id) || rec.Body.String() != id {
		t.Fatalf("header=%q body=%q", id, rec.Body.String())
	}
}

func TestRequestIDEchoedWhenValidReplacedWhenNot(t *testing.T) {
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "client-id-12345")
	rec := httptest.NewRecorder()
	RequestID(okHandler()).ServeHTTP(rec, req)
	if got := rec.Header().Get(RequestIDHeader); got != "client-id-12345" {
		t.Errorf("valid id not echoed: %q", got)
	}

	req = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set(RequestIDHeader, "bad id\twith spaces")
	rec = httptest.NewRecorder()
	RequestID(okHandler()).ServeHTTP(rec, req)
	if got := rec.Header().Get(RequestIDHeader); got == "bad id\twith spaces" || !validRequestID.MatchString(got) {
		t.Errorf("invalid id not replaced: %q", got)
	}
}

func TestRecoverReturnsStandardError(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("contains user data")
	}), RequestID, Recover(logger))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Code != "internal" || body.RequestID == "" || body.Retryable {
		t.Errorf("unexpected body: %+v", body)
	}
	if strings.Contains(logs.String(), "contains user data") {
		t.Error("panic value must not be logged")
	}
}

func TestNotFoundUsesErrorContract(t *testing.T) {
	rec := httptest.NewRecorder()
	Chain(http.HandlerFunc(NotFound), RequestID).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope", nil))
	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound || body.Code != "not_found" {
		t.Errorf("code=%d body=%+v", rec.Code, body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content type = %q", ct)
	}
}

func TestAccessLogOmitsQueryString(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := Chain(okHandler(), RequestID, AccessLog(logger))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/things?token=topsecret", nil))

	out := logs.String()
	if strings.Contains(out, "topsecret") {
		t.Fatalf("query string leaked: %s", out)
	}
	var line map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &line); err != nil {
		t.Fatalf("bad log line %q: %v", out, err)
	}
	if line["path"] != "/things" || line["status"] != float64(200) || line["method"] != "GET" {
		t.Errorf("unexpected log line: %v", line)
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	SecurityHeaders(okHandler()).ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("headers = %v", rec.Header())
	}
}

func TestLocalURL(t *testing.T) {
	cases := map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/healthz",
		"127.0.0.1:7000": "http://127.0.0.1:7000/healthz",
		"garbage":        "http://127.0.0.1:8080/healthz",
	}
	for in, want := range cases {
		if got := LocalURL(in, "/healthz"); got != want {
			t.Errorf("LocalURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestServerGracefulShutdownFinishesInflightRequest(t *testing.T) {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	started := make(chan struct{})
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "done")
	})
	srv := NewServer("127.0.0.1:0", h, Timeouts{ReadHeader: time.Second, Read: time.Second, Write: 5 * time.Second, Idle: time.Second}, logger)

	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()

	type result struct {
		body string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+ln.Addr().String()+"/", nil)
		if err != nil {
			resCh <- result{err: err}
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		resCh <- result{body: string(b), err: err}
	}()

	<-started
	shutdownDone := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdownDone <- srv.Shutdown(ctx)
	}()
	time.Sleep(50 * time.Millisecond)
	close(release)

	if err := <-shutdownDone; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	res := <-resCh
	if res.err != nil || res.body != "done" {
		t.Fatalf("in-flight request failed: body=%q err=%v", res.body, res.err)
	}
}
