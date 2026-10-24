package journal

import (
	"errors"
	"fmt"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"path/filepath"
	"slices"
	"testing"
)

func TestBatchValidationAndRecovery(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	j, _, _ := open(t, p, Options{})
	cs := sequenced(3)
	bad := append([]command.Command(nil), cs...)
	bad[2].Seq = 9
	if err := j.AppendBatch(bad); err == nil {
		t.Fatal("accepted invalid batch")
	}
	if size(t, p) != 0 {
		t.Fatal("partial write")
	}
	bad = append([]command.Command(nil), cs...)
	bad[2].Body = nil
	if err := j.AppendBatch(bad); err == nil {
		t.Fatal("accepted unencodable batch")
	}
	if size(t, p) != 0 {
		t.Fatal("partial encoding write")
	}
	if err := j.AppendBatch(cs); err != nil {
		t.Fatal(err)
	}
	j.Close()
	_, got, res := open(t, p, Options{})
	if len(got) != 3 || res.LastSeq != 3 {
		t.Fatalf("recovery: %v %+v", got, res)
	}
}
func TestBatchStickyFailure(t *testing.T) {
	j, _, _ := open(t, filepath.Join(t.TempDir(), FileName), Options{})
	j.f.Close()
	if err := j.AppendBatch(sequenced(2)); err == nil {
		t.Fatal("expected write error")
	}
	if err := j.AppendBatch(sequenced(2)); !errors.Is(err, ErrFailed) {
		t.Fatalf("not sticky: %v", err)
	}
}

type countedFile struct {
	writes, syncs int
	syncErr       error
}

func (f *countedFile) Write(p []byte) (int, error) { f.writes++; return len(p), nil }
func (f *countedFile) Sync() error                 { f.syncs++; return f.syncErr }
func (f *countedFile) Close() error                { return nil }
func TestBatchOneWriteOneSync(t *testing.T) {
	f := &countedFile{}
	j := &Journal{f: f, nextSeq: 1}
	if err := j.AppendBatch(sequenced(64)); err != nil {
		t.Fatal(err)
	}
	if f.writes != 1 || f.syncs != 1 {
		t.Fatalf("writes=%d syncs=%d", f.writes, f.syncs)
	}
}
func TestBatchSyncFailureSticky(t *testing.T) {
	f := &countedFile{syncErr: errors.New("sync failed")}
	j := &Journal{f: f, nextSeq: 1}
	if err := j.AppendBatch(sequenced(2)); err == nil {
		t.Fatal("expected sync error")
	}
	if err := j.AppendBatch(sequenced(2)); !errors.Is(err, ErrFailed) {
		t.Fatalf("not sticky: %v", err)
	}
	if f.writes != 1 || f.syncs != 1 || j.nextSeq != 1 {
		t.Fatal("retried failed batch")
	}
}

func TestBatchCrashHooksRemainFrameBoundaries(t *testing.T) {
	var events []string
	opts := Options{Hooks: Hooks{BeforeWrite: func(s domain.Seq) { events = append(events, fmt.Sprintf("before%d", s)) }, MidWrite: func(s domain.Seq) { events = append(events, fmt.Sprintf("mid%d", s)) }, AfterWrite: func(s domain.Seq) { events = append(events, fmt.Sprintf("write%d", s)) }, AfterSync: func(s domain.Seq) { events = append(events, fmt.Sprintf("sync%d", s)) }}}
	j, _, _ := open(t, filepath.Join(t.TempDir(), FileName), opts)
	if err := j.AppendBatch(sequenced(2)); err != nil {
		t.Fatal(err)
	}
	want := []string{"before1", "mid1", "write1", "before2", "mid2", "write2", "sync1", "sync2"}
	if !slices.Equal(events, want) {
		t.Fatalf("hooks %v", events)
	}
}
