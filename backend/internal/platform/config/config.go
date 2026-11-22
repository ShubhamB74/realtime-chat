// Package config loads and validates service configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
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

	// Authentication (ADR-009). JWTSecret is never logged.
	JWTSecret          string
	AuthIssuer         string
	AccessTokenTTL     time.Duration
	RefreshTokenTTL    time.Duration
	SessionMaxLifetime time.Duration
	MaxDevices         int
	Argon2MemoryKiB    uint32
	Argon2Time         uint32
	Argon2Threads      uint8
	HashConcurrency    int // 0 means one slot per CPU
	TrustProxyHeaders  bool

	// WebSocket gateway (docs/websocket-protocol.md). Defaults are unmeasured.
	GatewayID            string
	WSFlushInterval      time.Duration
	WSPresenceGrace      time.Duration
	WSMaxPresenceSubs    int
	WSAllowedOrigins     []string
	WSMaxConns           int
	WSMaxConnsPerUser    int
	WSSendQueue          int
	WSMaxQueueBytes      int64
	WSPingInterval       time.Duration
	WSPongTimeout        time.Duration
	WSWriteTimeout       time.Duration
	WSRevalidateInterval time.Duration
	WSReadLimitBytes     int64
	WSEventRate          int
	WSEventBurst         int

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

	num := func(key string, def, lo, hi int64) int64 {
		raw := strings.TrimSpace(getenv(key))
		if raw == "" {
			return def
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < lo || n > hi {
			errs = append(errs, fmt.Errorf("%s: must be an integer between %d and %d", key, lo, hi))
			return def
		}
		return n
	}

	cfg := Config{
		Env:                  str("APP_ENV", "development"),
		HTTPAddr:             str("HTTP_ADDR", ":8080"),
		LogLevel:             str("LOG_LEVEL", "info"),
		LogFormat:            str("LOG_FORMAT", "json"),
		DatabaseURL:          strings.TrimSpace(getenv("DATABASE_URL")),
		RedisURL:             strings.TrimSpace(getenv("REDIS_URL")),
		ObjectStoreAddr:      strings.TrimSpace(getenv("OBJECT_STORE_ADDR")),
		GatewayID:            strings.TrimSpace(getenv("GATEWAY_ID")),
		WSFlushInterval:      dur("WS_FLUSH_INTERVAL", 2*time.Second),
		WSPresenceGrace:      dur("WS_PRESENCE_GRACE", 8*time.Second),
		WSMaxPresenceSubs:    int(num("WS_MAX_PRESENCE_SUBS", 50, 1, 1000)),
		WSMaxConns:           int(num("WS_MAX_CONNS", 5000, 1, 1_000_000)),
		WSMaxConnsPerUser:    int(num("WS_MAX_CONNS_PER_USER", 5, 1, 100)),
		WSSendQueue:          int(num("WS_SEND_QUEUE", 256, 1, 100_000)),
		WSMaxQueueBytes:      num("WS_MAX_QUEUE_BYTES", 4<<20, 64<<10, 256<<20),
		WSPingInterval:       dur("WS_PING_INTERVAL", 25*time.Second),
		WSPongTimeout:        dur("WS_PONG_TIMEOUT", 10*time.Second),
		WSWriteTimeout:       dur("WS_WRITE_TIMEOUT", 10*time.Second),
		WSRevalidateInterval: dur("WS_REVALIDATE_INTERVAL", 60*time.Second),
		WSReadLimitBytes:     num("WS_READ_LIMIT_BYTES", 32768, 1024, 1<<20),
		WSEventRate:          int(num("WS_EVENT_RATE", 20, 1, 10_000)),
		WSEventBurst:         int(num("WS_EVENT_BURST", 40, 1, 10_000)),
		JWTSecret:            strings.TrimSpace(getenv("AUTH_JWT_SECRET")),
		AuthIssuer:           str("AUTH_ISSUER", "realtime-chat"),
		AccessTokenTTL:       dur("AUTH_ACCESS_TTL", 15*time.Minute),
		RefreshTokenTTL:      dur("AUTH_REFRESH_TTL", 30*24*time.Hour),
		SessionMaxLifetime:   dur("AUTH_SESSION_MAX_LIFETIME", 90*24*time.Hour),
		MaxDevices:           int(num("AUTH_MAX_DEVICES", 20, 1, 100)),
		Argon2MemoryKiB:      uint32(num("ARGON2_MEMORY_KIB", 64*1024, 8, 1<<20)), //nolint:gosec // range-checked above
		Argon2Time:           uint32(num("ARGON2_TIME", 3, 1, 10)),                //nolint:gosec // range-checked above
		Argon2Threads:        uint8(num("ARGON2_THREADS", 2, 1, 16)),              //nolint:gosec // range-checked above
		HashConcurrency:      int(num("ARGON2_MAX_CONCURRENT", 0, 0, 64)),
		TrustProxyHeaders:    boolEnv(getenv("TRUST_PROXY_HEADERS"), &errs),
		ShutdownTimeout:      dur("SHUTDOWN_TIMEOUT", 20*time.Second),
		ReadinessTimeout:     dur("READINESS_TIMEOUT", 2*time.Second),
		ReadHeaderTimeout:    dur("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
		ReadTimeout:          dur("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:         dur("HTTP_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:          dur("HTTP_IDLE_TIMEOUT", 60*time.Second),
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

	for _, o := range strings.Split(getenv("WS_ALLOWED_ORIGINS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			cfg.WSAllowedOrigins = append(cfg.WSAllowedOrigins, o)
		}
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

	if len(cfg.JWTSecret) < 32 {
		errs = append(errs, errors.New("AUTH_JWT_SECRET: required, at least 32 characters (run `make env` to generate one)"))
	}
	if cfg.GatewayID != "" && !gatewayIDRe.MatchString(cfg.GatewayID) {
		errs = append(errs, errors.New("GATEWAY_ID: 1 to 64 characters of letters, digits, dot, dash or underscore"))
	}
	if cfg.WSPongTimeout >= cfg.WSPingInterval {
		errs = append(errs, errors.New("WS_PONG_TIMEOUT: must be shorter than WS_PING_INTERVAL"))
	}
	if cfg.RefreshTokenTTL < cfg.AccessTokenTTL {
		errs = append(errs, errors.New("AUTH_REFRESH_TTL: must not be shorter than AUTH_ACCESS_TTL"))
	}
	if cfg.SessionMaxLifetime < cfg.RefreshTokenTTL {
		errs = append(errs, errors.New("AUTH_SESSION_MAX_LIFETIME: must not be shorter than AUTH_REFRESH_TTL"))
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
		slog.Duration("access_token_ttl", c.AccessTokenTTL),
		slog.Duration("refresh_token_ttl", c.RefreshTokenTTL),
		slog.Bool("trust_proxy_headers", c.TrustProxyHeaders),
		slog.Duration("shutdown_timeout", c.ShutdownTimeout),
	)
}

var gatewayIDRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func boolEnv(raw string, errs *[]error) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		*errs = append(*errs, errors.New("TRUST_PROXY_HEADERS: must be true or false"))
		return false
	}
	return b
}

func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "invalid"
	}
	return u.Redacted()
}
