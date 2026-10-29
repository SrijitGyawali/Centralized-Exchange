// Package config loads the server's startup configuration from environment
// variables into a typed, validated struct.
//
// Configuration is read once, in main, and passed down explicitly. No other
// package calls os.Getenv. That keeps every package testable and makes the
// full set of knobs visible in one place.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// Environment variable names. Keep .env.example in sync with this list.
const (
	EnvHTTPAddr        = "CEX_HTTP_ADDR"
	EnvDataDir         = "CEX_DATA_DIR"
	EnvLogLevel        = "CEX_LOG_LEVEL"
	EnvShutdownTimeout = "CEX_SHUTDOWN_TIMEOUT"
	EnvAdminAddr       = "CEX_ADMIN_ADDR"
)

// AdminOff is the CEX_ADMIN_ADDR value that disables the admin listener.
const AdminOff = "off"

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
	ShutdownTimeout   time.Duration
	Tokens            map[string]Identity
	BatchSize         int
	RatePerSecond     float64
	RateBurst         int
	RequestTimeout    time.Duration
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	// DatabaseURL enables the PostgreSQL read model. Empty means
	// no projection. It contains a password, so it is never logged.
	DatabaseURL string
	// AdminAddr is the PRIVATE operator listener for /metrics and
	// /debug/pprof. Default 127.0.0.1:6060: loopback only, so
	// nothing outside the machine can reach profiling endpoints. Empty
	// after Load means disabled (CEX_ADMIN_ADDR=off).
	AdminAddr string
	// QueueSize bounds the engine's intake queue (requests waiting for the
	// owner goroutine). A full queue refuses new requests with 503.
	QueueSize int
}

// Identity is provisioned by an operator, never supplied by an HTTP caller.
type Identity struct {
	User  domain.UserID
	Admin bool
}

// Default returns the configuration used when no variables are set.
// The defaults suit local development.
func Default() Config {
	return Config{
		HTTPAddr:        ":8080",
		DataDir:         "./data",
		LogLevel:        slog.LevelInfo,
		ShutdownTimeout: 10 * time.Second,
		BatchSize:       64, RatePerSecond: 100, RateBurst: 200,
		RequestTimeout: 5 * time.Second, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		AdminAddr: "127.0.0.1:6060", QueueSize: 1024,
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
	switch v := getenv(EnvAdminAddr); v {
	case "":
	case AdminOff:
		cfg.AdminAddr = ""
	default:
		cfg.AdminAddr = v
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

	for _, setting := range []struct {
		name string
		dst  *time.Duration
	}{
		{"CEX_REQUEST_TIMEOUT", &cfg.RequestTimeout}, {"CEX_READ_HEADER_TIMEOUT", &cfg.ReadHeaderTimeout},
		{"CEX_READ_TIMEOUT", &cfg.ReadTimeout}, {"CEX_WRITE_TIMEOUT", &cfg.WriteTimeout}, {"CEX_IDLE_TIMEOUT", &cfg.IdleTimeout},
	} {
		if v := getenv(setting.name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 || d > 24*time.Hour {
				errs = append(errs, fmt.Errorf("%s: must be a duration in (0, 24h]", setting.name))
			} else {
				*setting.dst = d
			}
		}
	}
	if cfg.WriteTimeout <= cfg.RequestTimeout {
		errs = append(errs, errors.New("CEX_WRITE_TIMEOUT: must exceed CEX_REQUEST_TIMEOUT"))
	}
	for _, setting := range []struct {
		name string
		dst  *int
		max  int
	}{
		{"CEX_BATCH_SIZE", &cfg.BatchSize, 1024}, {"CEX_RATE_BURST", &cfg.RateBurst, 1000000},
		{"CEX_QUEUE_SIZE", &cfg.QueueSize, 1000000},
	} {
		if v := getenv(setting.name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || n > setting.max {
				errs = append(errs, fmt.Errorf("%s: must be 1..%d", setting.name, setting.max))
			} else {
				*setting.dst = n
			}
		}
	}
	if v := getenv("CEX_RATE_PER_SECOND"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 || n > 1000000 {
			errs = append(errs, errors.New("CEX_RATE_PER_SECOND: must be finite and in (0, 1000000]"))
		} else {
			cfg.RatePerSecond = n
		}
	}
	if v := getenv("CEX_DATABASE_URL"); v != "" {
		// Only the shape is checked here; the error never quotes the value,
		// because it holds a password.
		if u, err := url.Parse(v); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
			errs = append(errs, errors.New("CEX_DATABASE_URL: must be postgres://user:password@host:port/dbname"))
		} else {
			cfg.DatabaseURL = v
		}
	}
	if v := getenv("CEX_API_TOKENS"); v != "" {
		var err error
		cfg.Tokens, err = parseTokens(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("CEX_API_TOKENS: %w", err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Config{}, fmt.Errorf("invalid configuration: %w", err)
	}
	return cfg, nil
}

// parseTokens deliberately returns structural errors only: JSON decoder errors
// can quote credential bytes. Duplicate members are rejected before decoding.
func parseTokens(input string) (map[string]Identity, error) {
	bad := errors.New("expected up to 1024 unique token records with token, user_id and optional admin")
	if len(input) > 1<<20 {
		return nil, bad
	}
	d := json.NewDecoder(strings.NewReader(input))
	start, err := d.Token()
	if err != nil || start != json.Delim('[') {
		return nil, bad
	}
	out := make(map[string]Identity)
	roles := make(map[domain.UserID]bool)
	for d.More() {
		if len(out) >= 1024 {
			return nil, bad
		}
		open, err := d.Token()
		if err != nil || open != json.Delim('{') {
			return nil, bad
		}
		fields := make(map[string]json.RawMessage)
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, bad
			}
			name, ok := k.(string)
			if !ok || (name != "token" && name != "user_id" && name != "admin") {
				return nil, bad
			}
			if _, exists := fields[name]; exists {
				return nil, bad
			}
			var raw json.RawMessage
			if err := d.Decode(&raw); err != nil || string(raw) == "null" {
				return nil, bad
			}
			fields[name] = raw
		}
		if end, err := d.Token(); err != nil || end != json.Delim('}') {
			return nil, bad
		}
		var token, user string
		var admin bool
		if json.Unmarshal(fields["token"], &token) != nil || json.Unmarshal(fields["user_id"], &user) != nil {
			return nil, bad
		}
		if raw, ok := fields["admin"]; ok {
			if json.Unmarshal(raw, &admin) != nil {
				return nil, bad
			}
		}
		if len(token) < 16 || len(token) > 256 {
			return nil, bad
		}
		for _, c := range token {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || strings.ContainsRune("-._~+/=", c)) {
				return nil, bad
			}
		}
		id, err := strconv.ParseUint(user, 10, 64)
		if err != nil || id == 0 || strconv.FormatUint(id, 10) != user {
			return nil, bad
		}
		if _, exists := out[token]; exists {
			return nil, bad
		}
		uid := domain.UserID(id)
		if previous, ok := roles[uid]; ok && previous != admin {
			return nil, errors.New("tokens for one user must have the same admin role")
		}
		roles[uid] = admin
		out[token] = Identity{User: uid, Admin: admin}
	}
	if end, err := d.Token(); err != nil || end != json.Delim(']') {
		return nil, bad
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, bad
	}
	return out, nil
}
