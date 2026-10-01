package config

import (
	"log/slog"
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
			want: Config{
				HTTPAddr:        "127.0.0.1:9000",
				DataDir:         "/var/lib/cex",
				LogLevel:        slog.LevelDebug,
				ShutdownTimeout: 30 * time.Second,
			},
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
			if got != tc.want {
				t.Errorf("Load() = %+v, want %+v", got, tc.want)
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
