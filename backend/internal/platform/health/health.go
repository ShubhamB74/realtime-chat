// Package health provides liveness and readiness endpoints.
//
// Liveness (/healthz) never touches dependencies. Readiness (/readyz) runs dependency checks
// concurrently under a timeout; only checks marked Required can make the service not ready.
package health

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"realtime-chat/backend/internal/platform/httpx"
)

// Check is one dependency probe.
type Check struct {
	Name     string
	Required bool
	Fn       func(ctx context.Context) error
}

// Handler serves readiness.
type Handler struct {
	checks   []Check
	timeout  time.Duration
	logger   *slog.Logger
	draining atomic.Bool
}

// New builds a readiness handler.
func New(checks []Check, timeout time.Duration, logger *slog.Logger) *Handler {
	return &Handler{checks: checks, timeout: timeout, logger: logger}
}

// SetDraining makes readiness fail immediately so load balancers stop sending traffic
// while in-flight requests finish.
func (h *Handler) SetDraining() { h.draining.Store(true) }

// Liveness reports that the process is up. It does not check dependencies.
func Liveness(w http.ResponseWriter, _ *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type checkResult struct {
	Status   string `json:"status"`
	Required bool   `json:"required"`
}

type readinessResponse struct {
	Status string                 `json:"status"`
	Checks map[string]checkResult `json:"checks"`
}

// Readiness runs all checks. Error details are logged, never returned to the caller.
func (h *Handler) Readiness(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		httpx.WriteJSON(w, http.StatusServiceUnavailable, readinessResponse{Status: "draining", Checks: map[string]checkResult{}})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	errs := make([]error, len(h.checks))
	var wg sync.WaitGroup
	for i, c := range h.checks {
		wg.Add(1)
		go func(i int, c Check) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					errs[i] = fmt.Errorf("check panicked: %T", rec)
				}
			}()
			errs[i] = c.Fn(ctx)
		}(i, c)
	}
	wg.Wait()

	resp := readinessResponse{Status: "ready", Checks: make(map[string]checkResult, len(h.checks))}
	for i, c := range h.checks {
		status := "up"
		if errs[i] != nil {
			status = "down"
			h.logger.WarnContext(r.Context(), "readiness check failed",
				slog.String("check", c.Name),
				slog.Bool("required", c.Required),
				slog.String("error", errs[i].Error()),
			)
			if c.Required {
				resp.Status = "not_ready"
			}
		}
		resp.Checks[c.Name] = checkResult{Status: status, Required: c.Required}
	}

	code := http.StatusOK
	if resp.Status != "ready" {
		code = http.StatusServiceUnavailable
	}
	httpx.WriteJSON(w, code, resp)
}

// TCPCheck succeeds if a TCP connection to addr can be opened.
func TCPCheck(addr string) func(context.Context) error {
	return func(ctx context.Context) error {
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		return conn.Close()
	}
}

// AnyTCPCheck succeeds if at least one address accepts a TCP connection.
func AnyTCPCheck(addrs []string) func(context.Context) error {
	return func(ctx context.Context) error {
		var errs []error
		for _, a := range addrs {
			err := TCPCheck(a)(ctx)
			if err == nil {
				return nil
			}
			errs = append(errs, err)
		}
		if len(errs) == 0 {
			return errors.New("no addresses configured")
		}
		return errors.Join(errs...)
	}
}
