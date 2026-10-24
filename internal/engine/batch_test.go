package engine

import (
	"errors"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
	"testing"
)

type batchStorage struct {
	batches [][]command.Command
	err     error
	entered chan struct{}
	release chan struct{}
}

func (s *batchStorage) Append(c command.Command) error { panic("batch storage used Append") }
func (s *batchStorage) AppendBatch(cs []command.Command) error {
	s.batches = append(s.batches, append([]command.Command(nil), cs...))
	if s.entered != nil {
		s.entered <- struct{}{}
		<-s.release
	}
	return s.err
}
func queueFunds(e *Engine, n int) []chan command.Result {
	rs := make([]chan command.Result, n)
	for i := range rs {
		rs[i] = make(chan command.Result, 1)
		e.intake <- request{cmd: command.Command{User: operator, Key: newKey(), Body: fundAlice(1)}, reply: rs[i]}
	}
	return rs
}
func TestBatchQueryBarrierAndClosedDrain(t *testing.T) {
	s := &batchStorage{}
	e := New(state.New(), Config{Journal: s, BatchSize: 64})
	rs := queueFunds(e, 3)
	var observed uint64
	e.intake <- request{query: func(st *state.State) { observed = uint64(st.LastSeq()) }}
	rs = append(rs, queueFunds(e, 2)...)
	e.Close()
	e.Run()
	if len(s.batches) != 2 || len(s.batches[0]) != 3 || len(s.batches[1]) != 2 || observed != 3 {
		t.Fatalf("batches %v observed %d", s.batches, observed)
	}
	for i, r := range rs {
		if got := <-r; uint64(got.Seq) != uint64(i+1) {
			t.Fatalf("seq %d", got.Seq)
		}
	}
}
func TestBatchWaitsForDurability(t *testing.T) {
	s := &batchStorage{entered: make(chan struct{}, 1), release: make(chan struct{})}
	e := New(state.New(), Config{Journal: s, BatchSize: 64})
	rs := queueFunds(e, 3)
	e.Close()
	go e.Run()
	<-s.entered
	if e.Snapshot().Seq != 0 {
		t.Fatal("applied before sync")
	}
	for _, r := range rs {
		select {
		case <-r:
			t.Fatal("reply before sync")
		default:
		}
	}
	close(s.release)
	<-e.Done()
	if e.Snapshot().Seq != 3 {
		t.Fatal("did not apply")
	}
}
func TestFailedBatchAppliesNone(t *testing.T) {
	s := &batchStorage{err: errors.New("sync failed")}
	e := New(state.New(), Config{Journal: s, BatchSize: 64})
	rs := queueFunds(e, 3)
	e.Close()
	e.Run()
	if e.Err() == nil || e.Snapshot().Seq != 0 {
		t.Fatal("failed batch applied")
	}
	for _, r := range rs {
		if len(r) != 0 {
			t.Fatal("failed batch replied")
		}
	}
}

type legacyStorage struct{ seqs []uint64 }

func (s *legacyStorage) Append(c command.Command) error {
	s.seqs = append(s.seqs, uint64(c.Seq))
	return nil
}
func TestLegacyJournalAndBatchLimit(t *testing.T) {
	s := &legacyStorage{}
	e := New(state.New(), Config{Journal: s, BatchSize: 2})
	rs := queueFunds(e, 5)
	e.Close()
	e.Run()
	if len(s.seqs) != 5 || e.Snapshot().Seq != 5 {
		t.Fatal("legacy journal did not drain")
	}
	for i, r := range rs {
		if got := <-r; uint64(got.Seq) != uint64(i+1) {
			t.Fatal("legacy sequence")
		}
	}
	bs := &batchStorage{}
	e = New(state.New(), Config{Journal: bs, BatchSize: 2})
	queueFunds(e, 5)
	e.Close()
	e.Run()
	if len(bs.batches) != 3 || len(bs.batches[2]) != 1 {
		t.Fatalf("batch limit: %v", bs.batches)
	}
}
func TestLoneBatchProgress(t *testing.T) {
	s := &batchStorage{}
	e := New(state.New(), Config{Journal: s, BatchSize: 64})
	runInBackground(t, e)
	r := mustSubmit(t, e, operator, newKey(), fundAlice(1))
	if r.Seq != 1 {
		t.Fatal("lone command")
	}
}
