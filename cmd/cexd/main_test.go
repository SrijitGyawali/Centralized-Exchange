package main

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer safe for concurrent use: run writes logs from
// its goroutine while the test reads them. Without the mutex, `go test -race`
// would (correctly) report a data race.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startServer runs the real server on a random free port ("127.0.0.1:0")
// with its data in a temp directory, waits until it is listening, and
// returns its address, its logs and a function that stops it like SIGTERM
// would and returns run's error.
func startServer(t *testing.T) (addr string, logs *syncBuffer, stop func() error) {
	return startServerWithEnv(t, nil)
}

func startServerWithEnv(t *testing.T, overrides map[string]string) (addr string, logs *syncBuffer, stop func() error) {
	t.Helper()
	env := map[string]string{"CEX_HTTP_ADDR": "127.0.0.1:0", "CEX_DATA_DIR": t.TempDir()}
	for key, value := range overrides {
		env[key] = value
	}
	logs = &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(k string) string { return env[k] }, logs) }()

	listening := regexp.MustCompile(`"msg":"http listening","addr":"([^"]+)"`)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if m := listening.FindStringSubmatch(logs.String()); m != nil {
			addr = m[1]
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatalf("server never started; logs:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return addr, logs, func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("run() did not return after cancel")
			return nil
		}
	}
}

func TestRunWiresAuthenticatedAPI(t *testing.T) {
	addr, logs, stop := startServerWithEnv(t, map[string]string{
		"CEX_API_TOKENS": `[{"token":"integration-admin-token","user_id":"7","admin":true}]`,
		"CEX_BATCH_SIZE": "8", "CEX_RATE_PER_SECOND": "0.001", "CEX_RATE_BURST": "2",
	})
	defer stop()
	client := &http.Client{Timeout: 3 * time.Second}
	for i, token := range []string{"", "integration-admin-token", "integration-admin-token", "integration-admin-token"} {
		req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/v1/session", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		want := []int{401, 200, 200, 429}[i]
		if resp.StatusCode != want {
			t.Fatalf("request %d status %d, want %d", i, resp.StatusCode, want)
		}
	}
	if strings.Contains(logs.String(), "integration-admin-token") {
		t.Fatal("startup logs expose token")
	}
}

// TestRunShutsDownGracefully: cancel the context the way SIGTERM would; run
// must return nil promptly, after draining the engine and closing the journal.
func TestRunShutsDownGracefully(t *testing.T) {
	_, logs, stop := startServer(t)
	if err := stop(); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
	if !strings.Contains(logs.String(), "engine drained and journal closed") {
		t.Errorf("shutdown did not close the node; logs:\n%s", logs.String())
	}
}

// TestRunRecoversThenReportsReady: on a fresh data directory, recovery finds
// nothing, logs it, and /readyz answers 200.
func TestRunRecoversThenReportsReady(t *testing.T) {
	addr, logs, stop := startServer(t)
	defer stop()

	if !strings.Contains(logs.String(), `"msg":"recovered","commands":0`) {
		t.Errorf("no recovery log line; logs:\n%s", logs.String())
	}
	resp, err := http.Get("http://" + addr + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/readyz = %d, want 200 after a successful recovery", resp.StatusCode)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	getenv := func(k string) string {
		return map[string]string{"CEX_SHUTDOWN_TIMEOUT": "-5s"}[k]
	}
	if err := run(context.Background(), getenv, &syncBuffer{}); err == nil {
		t.Fatal("run() = nil, want config error")
	}
}
