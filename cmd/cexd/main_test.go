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

	"github.com/coder/websocket"
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
	// Both listeners on random free ports, so tests never collide with each
	// other or with a server the developer is running.
	env := map[string]string{"CEX_HTTP_ADDR": "127.0.0.1:0", "CEX_ADMIN_ADDR": "127.0.0.1:0", "CEX_DATA_DIR": t.TempDir()}
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

// TestRunServesTheWebUIAndALongLivedStream checks the composed routes on the
// real server: the page at /, and a WebSocket stream that outlives the
// server's HTTP WriteTimeout (a deadline left on a hijacked connection would
// silently kill every live stream after a few seconds) and is closed with a
// reason on shutdown.
func TestRunServesTheWebUIAndALongLivedStream(t *testing.T) {
	addr, logs, stop := startServerWithEnv(t, map[string]string{
		"CEX_API_TOKENS":      `[{"token":"stream-admin-token-01","user_id":"1","admin":true}]`,
		"CEX_REQUEST_TIMEOUT": "1s", "CEX_WRITE_TIMEOUT": "2s",
	})
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET /: %v\nserver logs:\n%s", err, logs)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("GET / = %d %q, want the web page", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	ctx := context.Background()
	c, _, err := websocket.Dial(ctx, "ws://"+addr+"/stream/book", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	readMsg := func() string {
		t.Helper()
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, b, err := c.Read(rctx)
		if err != nil {
			t.Fatalf("reading the stream: %v", err)
		}
		return string(b)
	}
	readMsg() // the initial snapshot

	time.Sleep(3 * time.Second) // longer than CEX_WRITE_TIMEOUT
	req, _ := http.NewRequest("POST", "http://"+addr+"/v1/admin/fund", strings.NewReader(`{"user_id":"1","asset":"USDT","amount":"1"}`))
	req.Header.Set("Authorization", "Bearer stream-admin-token-01")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "fund-after-wait")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("funding: %v %v", resp, err)
	}
	if msg := readMsg(); !strings.Contains(msg, `"seq":"1"`) {
		t.Fatalf("after the wait: %s; want the update at seq 1 (the stream died?)", msg)
	}

	if err := stop(); err != nil {
		t.Fatal(err)
	}
	stopped = true
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(rctx); websocket.CloseStatus(err) != websocket.StatusTryAgainLater {
		t.Errorf("after shutdown: %v; want close 1013 (server shutting down)", err)
	}
}
