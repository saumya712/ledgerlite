// Package config loads settings from environment variables, so the same
// binary runs on a laptop, in Docker, and on a cloud platform without a rebuild.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr        string        // HTTP_ADDR, or ":" + PORT, default ":8080"
	DatabaseURL     string        // DATABASE_URL (required)
	DBMaxConns      int           // DB_MAX_CONNS, default 20
	LogLevel        slog.Level    // LOG_LEVEL: debug|info|warn|error, default info
	LogFormat       string        // LOG_FORMAT: json|text, default json
	ShutdownTimeout time.Duration // SHUTDOWN_TIMEOUT, default 15s
}

// Load reads and validates the environment. It reports every problem at once,
// so you fix them in one go instead of one restart per mistake.
// An empty variable counts as unset.
func Load() (Config, error) {
	var errs []error

	cfg := Config{
		DatabaseURL: get("DATABASE_URL", ""),
		LogFormat:   strings.ToLower(get("LOG_FORMAT", "json")),
	}
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}

	// Platforms like Railway inject PORT; HTTP_ADDR wins if both are set.
	cfg.HTTPAddr = get("HTTP_ADDR", "")
	if cfg.HTTPAddr == "" {
		if port := get("PORT", ""); port != "" {
			cfg.HTTPAddr = ":" + port
		} else {
			cfg.HTTPAddr = ":8080"
		}
	}

	var err error
	if cfg.DBMaxConns, err = intEnv("DB_MAX_CONNS", 20, 1, 500); err != nil {
		errs = append(errs, err)
	}
	if cfg.ShutdownTimeout, err = durationEnv("SHUTDOWN_TIMEOUT", 15*time.Second); err != nil {
		errs = append(errs, err)
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(get("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	if cfg.LogFormat != "json" && cfg.LogFormat != "text" {
		errs = append(errs, fmt.Errorf("LOG_FORMAT must be json or text, got %q", cfg.LogFormat))
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func get(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def, lo, hi int) (int, error) {
	raw := get(key, "")
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s must be an integer between %d and %d, got %q", key, lo, hi, raw)
	}
	return n, nil
}

func durationEnv(key string, def time.Duration) (time.Duration, error) {
	raw := get(key, "")
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration like 15s or 1m, got %q", key, raw)
	}
	return d, nil
}
