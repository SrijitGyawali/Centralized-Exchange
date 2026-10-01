// Package config loads the server's startup configuration from environment
// variables into a typed, validated struct.
//
// Configuration is read once, in main, and passed down explicitly. No other
// package calls os.Getenv. That keeps every package testable and makes the
// full set of knobs visible in one place.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Environment variable names. Keep .env.example in sync with this list.
const (
	EnvHTTPAddr        = "CEX_HTTP_ADDR"
	EnvDataDir         = "CEX_DATA_DIR"
	EnvLogLevel        = "CEX_LOG_LEVEL"
	EnvShutdownTimeout = "CEX_SHUTDOWN_TIMEOUT"
)

// Config is the complete startup configuration of cexd.
type Config struct {
	// HTTPAddr is the listen address, e.g. ":8080" or "127.0.0.1:0".
	HTTPAddr string
	// DataDir holds the WAL and, later, snapshots. It must be on a
	// persistent volume: losing it loses the exchange state.
	DataDir string
	// LogLevel is the minimum level written by the structured logger.
	LogLevel slog.Level
	// ShutdownTimeout bounds graceful shutdown: stop admitting, drain, flush.
	ShutdownTimeout time.Duration
}

// Default returns the configuration used when no variables are set.
// The defaults suit local development.
func Default() Config {
	return Config{
		HTTPAddr:        ":8080",
		DataDir:         "./data",
		LogLevel:        slog.LevelInfo,
		ShutdownTimeout: 10 * time.Second,
	}
}

// Load builds a Config from getenv, starting from Default.
//
// It takes a lookup function rather than calling os.Getenv directly. That is
// a small form of dependency injection: tests pass a map-backed function, and
// main passes os.Getenv.
//
// All problems are reported together (errors.Join) so an operator can fix a
// bad deployment in one pass instead of one restart per mistake.
func Load(getenv func(string) string) (Config, error) {
	cfg := Default()
	var errs []error

	if v := getenv(EnvHTTPAddr); v != "" {
		cfg.HTTPAddr = v
	}
	if v := getenv(EnvDataDir); v != "" {
		cfg.DataDir = v
	}
	if v := getenv(EnvLogLevel); v != "" {
		// slog.Level understands "debug", "info", "warn", "error"
		// (case-insensitive) and offsets such as "info+2".
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", EnvLogLevel, err))
		}
	}
	if v := getenv(EnvShutdownTimeout); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", EnvShutdownTimeout, err))
		case d <= 0:
			errs = append(errs, fmt.Errorf("%s: must be positive, got %s", EnvShutdownTimeout, d))
		default:
			cfg.ShutdownTimeout = d
		}
	}

	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}
