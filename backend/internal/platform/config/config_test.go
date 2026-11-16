package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func valid() map[string]string {
	return map[string]string{
		"DATABASE_URL":    "postgres://chat@localhost:5432/db?sslmode=disable",
		"REDIS_URL":       "redis://localhost:6379/0",
		"AUTH_JWT_SECRET": "0123456789abcdef0123456789abcdef0123",
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(valid()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Env != "development" || cfg.HTTPAddr != ":8080" || cfg.LogFormat != "json" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.ShutdownTimeout != 20*time.Second {
		t.Errorf("shutdown timeout = %v", cfg.ShutdownTimeout)
	}
	if len(cfg.KafkaBrokers) != 0 {
		t.Errorf("kafka brokers should be empty, got %v", cfg.KafkaBrokers)
	}
}

func TestLoadRequiresDatabaseAndRedis(t *testing.T) {
	_, err := Load(env(map[string]string{}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"DATABASE_URL: required", "REDIS_URL: required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := map[string]map[string]string{
		"bad env":      {"APP_ENV": "staging"},
		"bad format":   {"LOG_FORMAT": "xml"},
		"bad duration": {"SHUTDOWN_TIMEOUT": "soon"},
		"zero timeout": {"READINESS_TIMEOUT": "0s"},
		"bad db conns": {"DB_MAX_CONNS": "-3"},
		"bad db url":   {"DATABASE_URL": "mysql://x"},
		"bad redis":    {"REDIS_URL": "http://x"},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			m := valid()
			for k, v := range override {
				m[k] = v
			}
			if _, err := Load(env(m)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestProductionRejectsPlaintextDatabase(t *testing.T) {
	m := valid()
	m["APP_ENV"] = "production"
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "sslmode=disable") {
		t.Fatalf("expected sslmode error, got %v", err)
	}
	m["DATABASE_URL"] = "postgres://chat:x@db:5432/db?sslmode=require"
	if _, err := Load(env(m)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestErrorsDoNotLeakSecrets(t *testing.T) {
	m := valid()
	m["DATABASE_URL"] = "mysql://chat:s3cret@localhost/db"
	_, err := Load(env(m))
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("error must exist and not contain secret: %v", err)
	}
}

func TestKafkaBrokersParsed(t *testing.T) {
	m := valid()
	m["KAFKA_BROKERS"] = " a:9092, ,b:9092 "
	cfg, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.KafkaBrokers) != 2 || cfg.KafkaBrokers[0] != "a:9092" || cfg.KafkaBrokers[1] != "b:9092" {
		t.Errorf("brokers = %v", cfg.KafkaBrokers)
	}
}

func TestLogValueRedactsPasswords(t *testing.T) {
	cfg, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", slog.Any("config", cfg))
	if strings.Contains(buf.String(), "s3cret") {
		t.Fatalf("password leaked into logs: %s", buf.String())
	}
}

func TestAuthSecretRequiredAndNeverLogged(t *testing.T) {
	m := valid()
	delete(m, "AUTH_JWT_SECRET")
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "AUTH_JWT_SECRET") {
		t.Fatalf("missing secret must be rejected, got %v", err)
	}
	m["AUTH_JWT_SECRET"] = "too-short"
	if _, err := Load(env(m)); err == nil {
		t.Fatal("short secret must be rejected")
	}
	cfg, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", slog.Any("config", cfg))
	if strings.Contains(buf.String(), "0123456789abcdef") {
		t.Fatalf("JWT secret leaked into logs: %s", buf.String())
	}
}

func TestAuthDefaults(t *testing.T) {
	cfg, err := Load(env(valid()))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessTokenTTL != 15*time.Minute || cfg.RefreshTokenTTL != 30*24*time.Hour || cfg.MaxDevices != 20 {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.Argon2MemoryKiB != 64*1024 || cfg.Argon2Time != 3 || cfg.Argon2Threads != 2 || cfg.TrustProxyHeaders {
		t.Errorf("unexpected hashing defaults: %+v", cfg)
	}
}

func TestAuthValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"refresh shorter than access": {"AUTH_ACCESS_TTL": "2h", "AUTH_REFRESH_TTL": "1h"},
		"lifetime shorter than ttl":   {"AUTH_SESSION_MAX_LIFETIME": "1h"},
		"argon memory too small":      {"ARGON2_MEMORY_KIB": "1"},
		"argon threads too large":     {"ARGON2_THREADS": "99"},
		"bad max devices":             {"AUTH_MAX_DEVICES": "0"},
		"bad proxy flag":              {"TRUST_PROXY_HEADERS": "maybe"},
	}
	for name, override := range cases {
		t.Run(name, func(t *testing.T) {
			m := valid()
			for k, v := range override {
				m[k] = v
			}
			if _, err := Load(env(m)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	m := valid()
	m["TRUST_PROXY_HEADERS"] = "true"
	if cfg, err := Load(env(m)); err != nil || !cfg.TrustProxyHeaders {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}
