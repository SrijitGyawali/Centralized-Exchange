package engine

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// recorder is a hand-written fake Observer that keeps everything it is told.
// The mutex is needed because Refused runs on submitting goroutines while the
// rest runs on the owner goroutine.
type recorder struct {
	mu        sync.Mutex
	waits     []time.Duration
	durable   []int // commands per durable append
	durableD  []time.Duration
	applied   []command.Result
	completed []time.Duration
	queries   int
	refused   []error
}

func (r *recorder) QueueWait(d time.Duration) {
	r.mu.Lock()
	r.waits = append(r.waits, d)
	r.mu.Unlock()
}
func (r *recorder) Durable(n int, d time.Duration) {
	r.mu.Lock()
	r.durable, r.durableD = append(r.durable, n), append(r.durableD, d)
	r.mu.Unlock()
}
func (r *recorder) Applied(_ command.Body, res command.Result, _ time.Duration) {
	r.mu.Lock()
	r.applied = append(r.applied, res)
	r.mu.Unlock()
}
func (r *recorder) Completed(d time.Duration) {
	r.mu.Lock()
	r.completed = append(r.completed, d)
	r.mu.Unlock()
}
func (r *recorder) Query(time.Duration) { r.mu.Lock(); r.queries++; r.mu.Unlock() }
func (r *recorder) Refused(err error)   { r.mu.Lock(); r.refused = append(r.refused, err); r.mu.Unlock() }

// slowStorage is a batch journal whose "fsync" takes a known time.
type slowStorage struct{ delay time.Duration }

func (s slowStorage) Append(command.Command) error { panic("batch storage used Append") }
func (s slowStorage) AppendBatch([]command.Command) error {
	time.Sleep(s.delay)
	return nil
}

// TestObserverSeesEachPartOfACommandsLife checks that the engine reports
// queue wait, the shared durable append, the apply and the total, and that
// they add up the way the Observer documentation says.
func TestObserverSeesEachPartOfACommandsLife(t *testing.T) {
	rec := &recorder{}
	// Sleeps stand in for the disk and the queue. time.Sleep is timed by the
	// Go runtime's own clock (on Windows it advances in ~0.5 ms ticks) while
	// the engine measures with internal/clock, so the checks below allow a
	// quarter of slack instead of demanding the exact sleep.
	const fsync, queued = 20 * time.Millisecond, 10 * time.Millisecond
	e := New(state.New(), Config{Journal: slowStorage{delay: fsync}, BatchSize: 64, Observer: rec})

	// Three commands admitted while Run is not started: they must form ONE
	// batch, so one durable append covers all three.
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if _, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1)); err != nil {
				t.Error(err)
			}
		})
	}
	waitQueued(t, e, 3)
	time.Sleep(queued) // a measurable wait in the queue
	runInBackground(t, e)
	wg.Wait()
	if _, err := e.Balance(t.Context(), alice, domain.USDT); err != nil { // one query
		t.Fatal(err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.durable) != 1 || rec.durable[0] != 3 {
		t.Fatalf("durable appends = %v, want one append of 3 commands", rec.durable)
	}
	if rec.durableD[0] < fsync*3/4 {
		t.Errorf("durable time %s, want about the %s fsync", rec.durableD[0], fsync)
	}
	if len(rec.waits) != 4 { // 3 commands + 1 query
		t.Fatalf("queue waits = %d, want 4", len(rec.waits))
	}
	for _, w := range rec.waits[:3] {
		if w < queued*3/4 {
			t.Errorf("queue wait %s, want about the %s the commands sat in the queue", w, queued)
		}
	}
	if len(rec.applied) != 3 || len(rec.completed) != 3 || rec.queries != 1 {
		t.Fatalf("applied %d, completed %d, queries %d; want 3, 3, 1", len(rec.applied), len(rec.completed), rec.queries)
	}
	for i, res := range rec.applied {
		if !res.Accepted() || res.Seq != domain.Seq(i+1) {
			t.Errorf("applied[%d] = seq %d accepted %v", i, res.Seq, res.Accepted())
		}
	}
	// The total includes the queue wait AND the shared fsync.
	for _, c := range rec.completed {
		if c < (fsync+queued)*3/4 {
			t.Errorf("completed %s is shorter than queue wait + fsync", c)
		}
	}
	if len(rec.refused) != 0 {
		t.Errorf("refused %v, want none", rec.refused)
	}
}

// TestObserverSeesRefusals: a request turned away at admission is reported
// with the reason, so overload is visible in metrics, not only in logs.
func TestObserverSeesRefusals(t *testing.T) {
	rec := &recorder{}
	e := New(state.New(), Config{QueueSize: 1, Observer: rec}) // Run not started: the queue only fills

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = e.Submit(t.Context(), operator, newKey(), fundAlice(1))
	}()
	waitQueued(t, e, 1)
	if _, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1)); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("err = %v, want ErrOverloaded", err)
	}
	runInBackground(t, e)
	<-done
	e.Close()
	if _, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}

	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.refused) != 2 || !errors.Is(rec.refused[0], ErrOverloaded) || !errors.Is(rec.refused[1], ErrClosed) {
		t.Fatalf("refused = %v, want [overloaded, closed]", rec.refused)
	}
}

// TestQueueLenAndCap: saturation and the batch limit are readable without
// touching the owner goroutine.
func TestQueueLenAndCap(t *testing.T) {
	e := New(state.New(), Config{QueueSize: 4})
	if e.QueueCap() != 4 || e.QueueLen() != 0 {
		t.Fatalf("len %d cap %d, want 0 and 4", e.QueueLen(), e.QueueCap())
	}
	if e.BatchLimit() != DefaultConfig().BatchSize {
		t.Fatalf("batch limit %d, want the default %d", e.BatchLimit(), DefaultConfig().BatchSize)
	}
	go func() { _, _ = e.Submit(t.Context(), operator, newKey(), fundAlice(1)) }()
	waitQueued(t, e, 1)
	if e.QueueLen() != 1 {
		t.Fatalf("len %d after one admission, want 1", e.QueueLen())
	}
	runInBackground(t, e)
}
