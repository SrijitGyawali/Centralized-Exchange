package config

import (
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"
)

// env returns a getenv function backed by a map. This is why Load accepts a
// function instead of reading the real process environment.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// TestLoad is a table-driven test, the standard Go testing pattern: each row
// is a named case, and t.Run gives each one its own name in the output
// (go test -run 'TestLoad/defaults').
func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    Config
		wantErr bool
	}{
		{
			name: "defaults when nothing set",
			env:  nil,
			want: Default(),
		},
		{
			name: "all overrides",
			env: map[string]string{
				EnvHTTPAddr:        "127.0.0.1:9000",
				EnvDataDir:         "/var/lib/cex",
				EnvLogLevel:        "DEBUG",
				EnvShutdownTimeout: "30s",
			},
			want: func() Config {
				c := Default()
				c.HTTPAddr = "127.0.0.1:9000"
				c.DataDir = "/var/lib/cex"
				c.LogLevel = slog.LevelDebug
				c.ShutdownTimeout = 30 * time.Second
				return c
			}(),
		},
		{
			name:    "bad log level",
			env:     map[string]string{EnvLogLevel: "loud"},
			wantErr: true,
		},
		{
			name:    "unparseable timeout",
			env:     map[string]string{EnvShutdownTimeout: "ten seconds"},
			wantErr: true,
		},
		{
			name:    "non-positive timeout",
			env:     map[string]string{EnvShutdownTimeout: "0s"},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Load(env(tc.env))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Load() error = nil, want error (got config %+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Load() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestWeek5Config(t *testing.T) {
	c, err := Load(env(map[string]string{
		"CEX_API_TOKENS": `[{"token":"test-admin-token-0001","user_id":"1","admin":true},{"token":"test-user-token-00002","user_id":"2"}]`,
		"CEX_BATCH_SIZE": "32", "CEX_RATE_PER_SECOND": "12.5", "CEX_RATE_BURST": "25",
		"CEX_REQUEST_TIMEOUT": "2s", "CEX_READ_HEADER_TIMEOUT": "3s", "CEX_READ_TIMEOUT": "4s", "CEX_WRITE_TIMEOUT": "5s", "CEX_IDLE_TIMEOUT": "30s",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tokens) != 2 || c.Tokens["test-admin-token-0001"].User != 1 || !c.Tokens["test-admin-token-0001"].Admin || c.BatchSize != 32 || c.RatePerSecond != 12.5 || c.RateBurst != 25 || c.RequestTimeout != 2*time.Second || c.ReadHeaderTimeout != 3*time.Second || c.ReadTimeout != 4*time.Second || c.WriteTimeout != 5*time.Second || c.IdleTimeout != 30*time.Second {
		t.Fatal("overrides not applied")
	}
	for _, tc := range []struct{ key, value string }{
		{"CEX_BATCH_SIZE", "0"}, {"CEX_BATCH_SIZE", "1025"}, {"CEX_BATCH_SIZE", "x"},
		{"CEX_RATE_PER_SECOND", "NaN"}, {"CEX_RATE_PER_SECOND", "+Inf"}, {"CEX_RATE_PER_SECOND", "0"},
		{"CEX_RATE_BURST", "-1"}, {"CEX_RATE_BURST", "1000001"},
		{"CEX_REQUEST_TIMEOUT", "0s"}, {"CEX_READ_HEADER_TIMEOUT", "bad"}, {"CEX_WRITE_TIMEOUT", "1s"},
		{"CEX_API_TOKENS", `null`}, {"CEX_API_TOKENS", `{}`},
		{"CEX_API_TOKENS", `[{"token":"test-user-token-00001","user_id":"0"}]`},
		{"CEX_API_TOKENS", `[{"token":"test-user-token-00001","user_id":1}]`},
		{"CEX_API_TOKENS", `[{"token":"short","user_id":"1"}]`},
		{"CEX_API_TOKENS", `[{"token":"test-user-token-00001","user_id":"1","extra":true}]`},
		{"CEX_API_TOKENS", `[{"token":"test-user-token-00001","user_id":"1","admin":null}]`},
		{"CEX_API_TOKENS", `[{"token":"test-user-token-00001","user_id":"1","user_id":"2"}]`},
		{"CEX_API_TOKENS", `[{"token":"test-user-token-00001","user_id":"1"},{"token":"test-user-token-00001","user_id":"2"}]`},
		{"CEX_API_TOKENS", `[{"token":"test-user-token-00001","user_id":"1","admin":true},{"token":"test-user-token-00002","user_id":"1"}]`},
		{"CEX_API_TOKENS", `[{"token":"test-user-token-00001","user_id":"1"}] []`},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			_, err := Load(env(map[string]string{tc.key: tc.value}))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("wanted error naming %s, got %v", tc.key, err)
			}
			if strings.Contains(err.Error(), "test-user-token") {
				t.Fatal("error exposed token")
			}
		})
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{
		EnvLogLevel:        "loud",
		EnvShutdownTimeout: "-1s",
	}))
	if err == nil {
		t.Fatal("want error")
	}
	// Both variables must be named so the operator can fix them in one pass.
	for _, name := range []string{EnvLogLevel, EnvShutdownTimeout} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention %s", err, name)
		}
	}
}

func TestDatabaseURL(t *testing.T) {
	c, err := Load(env(map[string]string{"CEX_DATABASE_URL": "postgres://cex:secret-pw@localhost:5432/cex"}))
	if err != nil || c.DatabaseURL != "postgres://cex:secret-pw@localhost:5432/cex" {
		t.Fatalf("valid URL: %+v, %v", c.DatabaseURL, err)
	}
	if c, _ := Load(env(nil)); c.DatabaseURL != "" {
		t.Error("the projection must be off by default")
	}
	for _, bad := range []string{"mysql://u:secret-pw@h/db", "postgres://", "not a url secret-pw", "postgres:///db?password=secret-pw"} {
		_, err := Load(env(map[string]string{"CEX_DATABASE_URL": bad}))
		if err == nil || !strings.Contains(err.Error(), "CEX_DATABASE_URL") {
			t.Errorf("%q: error = %v, want one naming CEX_DATABASE_URL", bad, err)
		}
		if err != nil && strings.Contains(err.Error(), "secret-pw") {
			t.Errorf("%q: the error leaked the password: %v", bad, err)
		}
	}
}
