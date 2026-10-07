package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

var envKeys = []string{
	"DATABASE_URL", "HTTP_ADDR", "PORT", "DB_MAX_CONNS",
	"LOG_LEVEL", "LOG_FORMAT", "SHUTDOWN_TIMEOUT",
}

// clearEnv blanks every variable for the duration of one test (empty = unset).
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" || cfg.DBMaxConns != 20 || cfg.LogLevel != slog.LevelInfo ||
		cfg.LogFormat != "json" || cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoad_RequiresDatabaseURL(t *testing.T) {
	clearEnv(t)
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("err = %v, want a DATABASE_URL error", err)
	}
}

func TestLoad_PortFallbackAndOverride(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")

	t.Setenv("PORT", "9000")
	cfg, err := Load()
	if err != nil || cfg.HTTPAddr != ":9000" {
		t.Fatalf("PORT fallback: addr = %q, err = %v", cfg.HTTPAddr, err)
	}

	t.Setenv("HTTP_ADDR", "127.0.0.1:7000")
	cfg, err = Load()
	if err != nil || cfg.HTTPAddr != "127.0.0.1:7000" {
		t.Fatalf("HTTP_ADDR should win: addr = %q, err = %v", cfg.HTTPAddr, err)
	}
}

func TestLoad_Overrides(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("DB_MAX_CONNS", "50")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "TEXT")
	t.Setenv("SHUTDOWN_TIMEOUT", "30s")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DBMaxConns != 50 || cfg.LogLevel != slog.LevelDebug ||
		cfg.LogFormat != "text" || cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoad_ReportsEveryProblemAtOnce(t *testing.T) {
	clearEnv(t)
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("DB_MAX_CONNS", "abc")
	t.Setenv("LOG_LEVEL", "loud")
	t.Setenv("LOG_FORMAT", "xml")
	t.Setenv("SHUTDOWN_TIMEOUT", "soon")

	_, err := Load()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{"DB_MAX_CONNS", "LOG_LEVEL", "LOG_FORMAT", "SHUTDOWN_TIMEOUT"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}
