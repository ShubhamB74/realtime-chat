package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func logger() *slog.Logger { return slog.New(slog.NewJSONHandler(io.Discard, nil)) }

func up(context.Context) error   { return nil }
func down(context.Context) error { return errors.New("connection refused: host=secret-host") }

func call(t *testing.T, h *Handler) (int, readinessResponse, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Readiness(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var resp readinessResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	return rec.Code, resp, rec.Body.String()
}

func TestLiveness(t *testing.T) {
	rec := httptest.NewRecorder()
	Liveness(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestReadinessAllUp(t *testing.T) {
	h := New([]Check{{"postgres", true, up}, {"redis", true, up}}, time.Second, logger())
	code, resp, _ := call(t, h)
	if code != http.StatusOK || resp.Status != "ready" || resp.Checks["postgres"].Status != "up" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestReadinessRequiredDownFailsAndHidesDetails(t *testing.T) {
	h := New([]Check{{"postgres", true, down}, {"redis", true, up}}, time.Second, logger())
	code, resp, raw := call(t, h)
	if code != http.StatusServiceUnavailable || resp.Status != "not_ready" || resp.Checks["postgres"].Status != "down" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
	if strings.Contains(raw, "secret-host") {
		t.Error("error details must not be returned to clients")
	}
}

func TestReadinessOptionalDownStaysReady(t *testing.T) {
	h := New([]Check{{"postgres", true, up}, {"kafka", false, down}}, time.Second, logger())
	code, resp, _ := call(t, h)
	if code != http.StatusOK || resp.Status != "ready" || resp.Checks["kafka"].Status != "down" || resp.Checks["kafka"].Required {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestReadinessTimeoutCountsAsDown(t *testing.T) {
	slow := func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	h := New([]Check{{"postgres", true, slow}}, 50*time.Millisecond, logger())
	code, resp, _ := call(t, h)
	if code != http.StatusServiceUnavailable || resp.Checks["postgres"].Status != "down" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestReadinessPanickingCheckIsContained(t *testing.T) {
	boom := func(context.Context) error { panic("boom") }
	h := New([]Check{{"postgres", true, boom}}, time.Second, logger())
	code, resp, _ := call(t, h)
	if code != http.StatusServiceUnavailable || resp.Checks["postgres"].Status != "down" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestReadinessDraining(t *testing.T) {
	h := New([]Check{{"postgres", true, up}}, time.Second, logger())
	h.SetDraining()
	code, resp, _ := call(t, h)
	if code != http.StatusServiceUnavailable || resp.Status != "draining" {
		t.Fatalf("code=%d resp=%+v", code, resp)
	}
}

func TestTCPChecks(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	live := ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	if err := TCPCheck(live)(context.Background()); err != nil {
		t.Errorf("live address should pass: %v", err)
	}
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := TCPCheck(live)(ctx); err == nil {
		t.Error("closed address should fail")
	}
	if err := AnyTCPCheck(nil)(ctx); err == nil {
		t.Error("empty address list should fail")
	}
}
