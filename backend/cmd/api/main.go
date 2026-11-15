// Command api is the Phase 1 backend entry point: configuration, logging, dependency
// connections, health/readiness endpoints and graceful shutdown. Business endpoints arrive in
// later phases.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"realtime-chat/backend/internal/platform/config"
	"realtime-chat/backend/internal/platform/health"
	"realtime-chat/backend/internal/platform/httpx"
	"realtime-chat/backend/internal/platform/logging"
	"realtime-chat/backend/internal/store/postgres"
	redisstore "realtime-chat/backend/internal/store/redis"
)

func main() { os.Exit(run()) }

func run() int {
	healthcheck := flag.Bool("healthcheck", false, "probe /healthz on the local listener and exit (used by container health checks)")
	flag.Parse()

	if *healthcheck {
		return probe(envOr("HTTP_ADDR", ":8080"))
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error:\n%v\n", err)
		return 2
	}
	logger, err := logging.New(os.Stdout, cfg.LogLevel, cfg.LogFormat)
	if err != nil {
		fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		return 2
	}
	slog.SetDefault(logger)
	logger.Info("starting api", slog.Any("config", cfg))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := postgres.Connect(ctx, cfg.DatabaseURL, cfg.DBMaxConns, logger)
	if err != nil {
		logger.Error("postgres unavailable", slog.String("error", err.Error()))
		return 1
	}
	defer pool.Close()

	rdb, err := redisstore.Connect(ctx, cfg.RedisURL, logger)
	if err != nil {
		logger.Error("redis unavailable", slog.String("error", err.Error()))
		return 1
	}
	defer func() { _ = rdb.Close() }()

	checks := []health.Check{
		{Name: "postgres", Required: true, Fn: pool.Ping},
		{Name: "redis", Required: true, Fn: func(ctx context.Context) error { return rdb.Ping(ctx).Err() }},
	}
	if len(cfg.KafkaBrokers) > 0 {
		checks = append(checks, health.Check{Name: "kafka", Required: false, Fn: health.AnyTCPCheck(cfg.KafkaBrokers)})
	}
	if cfg.ObjectStoreAddr != "" {
		checks = append(checks, health.Check{Name: "objectstore", Required: false, Fn: health.TCPCheck(cfg.ObjectStoreAddr)})
	}
	readiness := health.New(checks, cfg.ReadinessTimeout, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health.Liveness)
	mux.HandleFunc("GET /readyz", readiness.Readiness)
	mux.HandleFunc("/", httpx.NotFound)

	handler := httpx.Chain(mux,
		httpx.RequestID,
		httpx.AccessLog(logger),
		httpx.Recover(logger),
		httpx.SecurityHeaders,
	)
	srv := httpx.NewServer(cfg.HTTPAddr, handler, httpx.Timeouts{
		ReadHeader: cfg.ReadHeaderTimeout,
		Read:       cfg.ReadTimeout,
		Write:      cfg.WriteTimeout,
		Idle:       cfg.IdleTimeout,
	}, logger)

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", cfg.HTTPAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverErr:
		if err != nil {
			logger.Error("http server failed", slog.String("error", err.Error()))
			return 1
		}
	}

	// Fail readiness first so load balancers stop routing, then drain in-flight requests.
	readiness.SetDraining()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", slog.String("error", err.Error()))
		_ = srv.Close()
		return 1
	}
	logger.Info("shutdown complete")
	return 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// probe performs a local liveness request and returns a process exit code.
func probe(addr string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpx.LocalURL(addr, "/healthz"), nil)
	if err != nil {
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
