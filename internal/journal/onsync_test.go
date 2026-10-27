package journal

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// slowSyncFile is a file whose fsync takes a known time, so the test can
// check the split between write time and sync time.
type slowSyncFile struct {
	countedFile
	delay time.Duration
}

func (f *slowSyncFile) Sync() error {
	time.Sleep(f.delay)
	return f.countedFile.Sync()
}

// TestOnSyncReportsBytesWriteAndSync: one callback per successful append,
// with the exact byte count and the fsync time on the sync side of the split.
func TestOnSyncReportsBytesWriteAndSync(t *testing.T) {
	type call struct {
		bytes       int
		write, sync time.Duration
	}
	var calls []call
	f := &slowSyncFile{delay: 20 * time.Millisecond}
	j := &Journal{f: f, nextSeq: 1, onSync: func(n int, w, s time.Duration) { calls = append(calls, call{n, w, s}) }}

	cmds := sequenced(3)
	if err := j.AppendBatch(cmds); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("%d callbacks for one batch, want 1", len(calls))
	}
	if want := len(encodeFrames(t, cmds)); calls[0].bytes != want {
		t.Errorf("bytes = %d, want %d (three frames)", calls[0].bytes, want)
	}
	// The fake fsync is a time.Sleep, which the Go runtime times with its own
	// clock; on Windows that clock advances in ~0.5 ms ticks, while OnSync
	// measures with internal/clock. The two can disagree by up to a tick, so
	// a 20 ms sleep may measure 19.6 ms: allow a quarter.
	if calls[0].sync < f.delay*3/4 {
		t.Errorf("sync = %s, want about the %s fsync", calls[0].sync, f.delay)
	}
	if calls[0].write >= f.delay/2 {
		t.Errorf("write = %s: the fsync leaked into the write time", calls[0].write)
	}

	// A failed sync reports nothing: the callback describes durable appends only.
	f.syncErr = errors.New("disk gone")
	if err := j.AppendBatch(sequenced(1)); err == nil {
		t.Fatal("expected the sync error")
	}
	if len(calls) != 1 {
		t.Errorf("%d callbacks after a failed sync, want still 1", len(calls))
	}
}

// TestOnSyncIsWiredThroughOpen: the option reaches a real journal on disk.
func TestOnSyncIsWiredThroughOpen(t *testing.T) {
	var total int
	j, _, _ := open(t, filepath.Join(t.TempDir(), FileName), Options{OnSync: func(n int, _, _ time.Duration) { total += n }})
	appendAll(t, j, sequenced(4))
	if want := len(encodeFrames(t, sequenced(4))); total != want {
		t.Fatalf("bytes reported %d, want %d", total, want)
	}
}
