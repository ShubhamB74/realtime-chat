// Package config loads and validates service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config holds validated runtime configuration. It never logs secrets: use LogValue.
type Config struct {
	Env       string
	HTTPAddr  string
	LogLevel  string
	LogFormat string

	DatabaseURL string
	DBMaxConns  int32 // 0 means "use the driver default"
	RedisURL    string

	// Optional dependencies: reported by /readyz but never fail readiness.
	KafkaBrokers    []string
	ObjectStoreAddr string

	ShutdownTimeout   time.Duration
	ReadinessTimeout  time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}

// Load builds a Config from getenv (os.Getenv in production, a map lookup in tests).
// All problems are reported together. Error text never contains URL contents.
func Load(getenv func(string) string) (Config, error) {
	var errs []error

	str := func(key, def string) string {
		if v := strings.TrimSpace(getenv(key)); v != "" {
			return v
		}
		return def
	}
	dur := func(key string, def time.Duration) time.Duration {
		raw := strings.TrimSpace(getenv(key))
		if raw == "" {
			return def
		}
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: must be a positive duration (got %q)", key, raw))
			return def
		}
		return d
	}

	cfg := Config{
		Env:               str("APP_ENV", "development"),
		HTTPAddr:          str("HTTP_ADDR", ":8080"),
		LogLevel:          str("LOG_LEVEL", "info"),
		LogFormat:         str("LOG_FORMAT", "json"),
		DatabaseURL:       strings.TrimSpace(getenv("DATABASE_URL")),
		RedisURL:          strings.TrimSpace(getenv("REDIS_URL")),
		ObjectStoreAddr:   strings.TrimSpace(getenv("OBJECT_STORE_ADDR")),
		ShutdownTimeout:   dur("SHUTDOWN_TIMEOUT", 20*time.Second),
		ReadinessTimeout:  dur("READINESS_TIMEOUT", 2*time.Second),
		ReadHeaderTimeout: dur("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:       dur("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:      dur("HTTP_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:       dur("HTTP_IDLE_TIMEOUT", 60*time.Second),
	}

	switch cfg.Env {
	case "development", "test", "production":
	default:
		errs = append(errs, fmt.Errorf("APP_ENV: must be development, test or production (got %q)", cfg.Env))
	}
	switch cfg.LogFormat {
	case "json", "text":
	default:
		errs = append(errs, fmt.Errorf("LOG_FORMAT: must be json or text (got %q)", cfg.LogFormat))
	}

	for _, b := range strings.Split(getenv("KAFKA_BROKERS"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			cfg.KafkaBrokers = append(cfg.KafkaBrokers, b)
		}
	}

	if raw := strings.TrimSpace(getenv("DB_MAX_CONNS")); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 {
			errs = append(errs, errors.New("DB_MAX_CONNS: must be a positive integer"))
		} else {
			cfg.DBMaxConns = int32(n)
		}
	}

	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL: required"))
	} else if u, err := url.Parse(cfg.DatabaseURL); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		errs = append(errs, errors.New("DATABASE_URL: must be a postgres:// or postgresql:// URL"))
	} else if cfg.Env == "production" && u.Query().Get("sslmode") == "disable" {
		errs = append(errs, errors.New("DATABASE_URL: sslmode=disable is not allowed when APP_ENV=production"))
	}

	if cfg.RedisURL == "" {
		errs = append(errs, errors.New("REDIS_URL: required"))
	} else if u, err := url.Parse(cfg.RedisURL); err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") {
		errs = append(errs, errors.New("REDIS_URL: must be a redis:// or rediss:// URL"))
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return cfg, nil
}

// LogValue implements slog.LogValuer and redacts credentials.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("env", c.Env),
		slog.String("http_addr", c.HTTPAddr),
		slog.String("log_level", c.LogLevel),
		slog.String("database", redact(c.DatabaseURL)),
		slog.String("redis", redact(c.RedisURL)),
		slog.Any("kafka_brokers", c.KafkaBrokers),
		slog.String("object_store_addr", c.ObjectStoreAddr),
		slog.Duration("shutdown_timeout", c.ShutdownTimeout),
	)
}

func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	return u.Redacted()
}
