package projector

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRunCountsFailuresWithoutADatabase: Run keeps retrying a database it
// cannot reach, reports itself unhealthy, and counts each failed attempt, so
// an outage shows up in metrics as a rising number. No Docker needed: the
// pool points at a closed local port.
func TestRunCountsFailuresWithoutADatabase(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://cex:unused@127.0.0.1:1/cex?connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	status := &Status{}
	status.healthy.Store(true) // pretend it was healthy before the outage
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		Run(ctx, Config{JournalPath: filepath.Join(t.TempDir(), "journal.wal"), Pool: pool}, 0, status,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()

	deadline := time.Now().Add(15 * time.Second)
	for status.Failures() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no failure counted while the database is unreachable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.Healthy() {
		t.Error("Healthy() = true while the database is unreachable")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
