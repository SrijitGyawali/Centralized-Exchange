package main

import (
	"bytes"
	"context"
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

// TestRunShutsDownGracefully starts the real server on a random free port
// (":0"), cancels its context the way a SIGTERM would, and checks that run
// returns nil promptly.
func TestRunShutsDownGracefully(t *testing.T) {
	getenv := func(k string) string {
		return map[string]string{"CEX_HTTP_ADDR": "127.0.0.1:0"}[k]
	}
	var logs syncBuffer
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- run(ctx, getenv, &logs) }()

	// Wait until the server reports it is listening, then "send SIGTERM".
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "http listening") {
		if time.Now().After(deadline) {
			t.Fatalf("server never started; logs:\n%s", logs.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run() did not return after cancel")
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
