package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// --- helpers ---------------------------------------------------------------

const (
	operator domain.UserID = 99
	alice    domain.UserID = 1
)

func usdt(n int64) int64 { return n * domain.AtomsPerUSDT }

func buy(price, qty int64, tif domain.TimeInForce) command.PlaceOrder {
	return command.PlaceOrder{Side: domain.Buy, Price: domain.Price(price), Qty: domain.Qty(qty), TIF: tif}
}

func fundAlice(amount int64) command.Fund {
	return command.Fund{To: alice, Asset: domain.USDT, Amount: amount}
}

var keyN atomic.Uint64

// newKey returns a never-before-used idempotency key.
func newKey() string { return fmt.Sprintf("k%d", keyN.Add(1)) }

// runInBackground starts the owner goroutine and makes sure the engine is
// closed and fully drained when the test ends, so no goroutine outlives it.
func runInBackground(t *testing.T, e *Engine) {
	t.Helper()
	go e.Run()
	t.Cleanup(func() {
		e.Close()
		<-e.Done()
	})
}

func started(t *testing.T, cfg Config) *Engine {
	t.Helper()
	e := New(state.New(), cfg)
	runInBackground(t, e)
	return e
}

func mustSubmit(t *testing.T, e *Engine, user domain.UserID, key string, body command.Body) command.Result {
	t.Helper()
	res, err := e.Submit(t.Context(), user, key, body)
	if err != nil {
		t.Fatalf("Submit(%v, %q, %#v): %v", user, key, body, err)
	}
	return res
}

// waitQueued waits until n requests sit in the intake queue of an engine
// whose Run has not started, so a test knows they were admitted.
func waitQueued(t *testing.T, e *Engine, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for len(e.intake) < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d requests were queued", len(e.intake), n)
		}
		time.Sleep(time.Millisecond)
	}
}

// --- the happy path --------------------------------------------------------

func TestSubmitSequencesAndApplies(t *testing.T) {
	e := started(t, DefaultConfig())

	r1 := mustSubmit(t, e, operator, newKey(), fundAlice(usdt(10_000)))
	r2 := mustSubmit(t, e, alice, newKey(), buy(60_000, 1_000, domain.GTC))

	if r1.Seq != 1 || r2.Seq != 2 {
		t.Errorf("Seqs = %d, %d; want 1, 2: the engine numbers commands in order", r1.Seq, r2.Seq)
	}
	if !r2.Accepted() || r2.Order != 1 || r2.Outcome != book.Rested {
		t.Errorf("order result = %+v, want accepted order 1, rested", r2)
	}
	// The snapshot is published before the reply, so it already shows the order.
	snap := e.Snapshot()
	want := []book.LevelSummary{{Price: 60_000, Qty: 1_000, Orders: 1}}
	if snap.Seq != 2 || !slices.Equal(snap.Book.Bids, want) {
		t.Errorf("snapshot = %+v, want seq 2 with bid %+v", snap, want)
	}
}

func TestBalanceQueryReadsYourWrites(t *testing.T) {
	e := started(t, DefaultConfig())
	mustSubmit(t, e, operator, newKey(), fundAlice(usdt(10_000)))
	placed := mustSubmit(t, e, alice, newKey(), buy(60_000, 1_000, domain.GTC))

	// Queries share the queue with commands, so a query sent after a
	// command's reply always sees that command's effect.
	bal, err := e.Balance(t.Context(), alice, domain.USDT)
	if err != nil {
		t.Fatal(err)
	}
	want := Balance{Available: usdt(4_000), Reserved: usdt(6_000), AsOf: placed.Seq}
	if bal != want {
		t.Errorf("Balance = %+v, want %+v", bal, want)
	}
}

func TestSnapshotsAreImmutable(t *testing.T) {
	e := started(t, DefaultConfig())
	mustSubmit(t, e, operator, newKey(), fundAlice(usdt(100_000)))
	mustSubmit(t, e, alice, newKey(), buy(60_000, 1_000, domain.GTC))

	old := e.Snapshot()
	frozen := Snapshot{Seq: old.Seq, Book: book.Depth{Bids: slices.Clone(old.Book.Bids), Asks: slices.Clone(old.Book.Asks)}}

	mustSubmit(t, e, alice, newKey(), buy(59_000, 2_000, domain.GTC))
	mustSubmit(t, e, alice, newKey(), command.CancelOrder{Order: 1})

	if now := e.Snapshot(); now == old || now.Seq <= old.Seq {
		t.Errorf("no new snapshot was published: old seq %d, now seq %d", old.Seq, now.Seq)
	}
	if !reflect.DeepEqual(*old, frozen) {
		t.Errorf("a published snapshot changed after later commands:\n now  %+v\n was  %+v", *old, frozen)
	}
}

// --- rejected before admission: definitely did NOT happen -------------------

func TestNilBodyIsNotAdmitted(t *testing.T) {
	e := started(t, DefaultConfig())
	if _, err := e.Submit(t.Context(), alice, newKey(), nil); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("error = %v, want ErrInvalidCommand", err)
	}
	if seq := e.Snapshot().Seq; seq != 0 {
		t.Errorf("a rejected request consumed Seq %d", seq)
	}
}

func TestContextDoneBeforeSubmitIsNotAdmitted(t *testing.T) {
	e := started(t, DefaultConfig())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := e.Submit(ctx, operator, newKey(), fundAlice(1))
	if !errors.Is(err, context.Canceled) || errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("error = %v, want plain context.Canceled (never admitted, so the outcome is known)", err)
	}
	if r := mustSubmit(t, e, operator, newKey(), fundAlice(1)); r.Seq != 1 {
		t.Errorf("next command got Seq %d, want 1: nothing should have run before it", r.Seq)
	}
}

func TestOverloadIsRejectedBeforeAdmission(t *testing.T) {
	e := New(state.New(), Config{QueueSize: 2}) // Run not started yet: the queue only fills

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1)); err != nil {
				t.Error(err) // t.Error, not t.Fatal: this is not the test's own goroutine
			}
		})
	}
	waitQueued(t, e, 2)

	if _, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1)); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("third request on a full queue: error = %v, want ErrOverloaded", err)
	}

	runInBackground(t, e)
	wg.Wait()
	if seq := e.Snapshot().Seq; seq != 2 {
		t.Errorf("Seq = %d after draining, want 2: the overloaded request must never run", seq)
	}
}

// --- admitted but unanswered: the outcome is UNKNOWN ------------------------

// The "lost response" story through the engine: the caller gives up while
// its command waits in the queue. The command still runs later, and a retry
// with the same key reveals that it did.
func TestTimeoutAfterAdmissionIsUnknownButTheCommandStillRuns(t *testing.T) {
	e := New(state.New(), DefaultConfig()) // not running: the command waits in the queue
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	_, err := e.Submit(ctx, operator, "fund-1", fundAlice(usdt(10_000)))
	if !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want ErrOutcomeUnknown wrapping context.DeadlineExceeded", err)
	}

	runInBackground(t, e) // now the queued command runs; its reply goes to a buffered channel nobody reads

	retry := mustSubmit(t, e, operator, "fund-1", fundAlice(usdt(10_000)))
	if !retry.Duplicate || retry.Seq != 1 {
		t.Errorf("retry = %+v, want a duplicate of seq 1: the abandoned command did run", retry)
	}
	if bal, _ := e.Balance(t.Context(), alice, domain.USDT); bal.Available != usdt(10_000) {
		t.Errorf("Alice has %d, want 10,000 USDT funded exactly once", bal.Available)
	}
}

// --- shutdown and halting --------------------------------------------------

func TestCloseDrainsAdmittedAndRefusesNew(t *testing.T) {
	e := New(state.New(), DefaultConfig())
	results := make([]command.Result, 3)
	var wg sync.WaitGroup
	for i := range 3 {
		wg.Go(func() {
			res, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1))
			if err != nil {
				t.Error(err)
			}
			results[i] = res // each goroutine writes its own index: no data race
		})
	}
	waitQueued(t, e, 3)

	e.Close()
	e.Close() // a second Close is harmless
	if _, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Submit after Close: error = %v, want ErrClosed", err)
	}

	go e.Run()
	wg.Wait()
	select {
	case <-e.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after draining")
	}
	for i, r := range results {
		if !r.Accepted() {
			t.Errorf("admitted request %d was not carried out: %+v", i, r)
		}
	}
}

func TestHaltOnApplyError(t *testing.T) {
	st := state.New()
	for seq := domain.Seq(1); seq <= 3; seq++ {
		if _, err := st.Apply(command.Command{Seq: seq, User: operator, Key: newKey(), Body: fundAlice(1)}); err != nil {
			t.Fatal(err)
		}
	}
	e := New(st, DefaultConfig())
	e.seq = 0 // sabotage BEFORE Run starts (so no race): the next command gets Seq 1, which State refuses
	runInBackground(t, e)

	_, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1))
	if !errors.Is(err, ErrOutcomeUnknown) || !errors.Is(err, ErrHalted) {
		t.Fatalf("error = %v, want ErrOutcomeUnknown wrapping ErrHalted", err)
	}
	if !errors.Is(e.Err(), state.ErrSeqNotIncreasing) {
		t.Errorf("Err() = %v, want it to wrap state.ErrSeqNotIncreasing", e.Err())
	}

	// From now on nothing is admitted, so the outcome is known: it did not run.
	if _, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1)); !errors.Is(err, ErrHalted) || errors.Is(err, ErrOutcomeUnknown) {
		t.Errorf("Submit after halt: error = %v, want plain ErrHalted", err)
	}
	if _, err := e.Balance(t.Context(), alice, domain.USDT); !errors.Is(err, ErrHalted) {
		t.Errorf("query after halt: error = %v, want ErrHalted", err)
	}
}

func TestHaltOnPanic(t *testing.T) {
	e := started(t, DefaultConfig())

	_, err := Query(t.Context(), e, func(StateReader) int { panic("boom") })
	if !errors.Is(err, ErrHalted) {
		t.Fatalf("error = %v, want ErrHalted", err)
	}
	if e.Err() == nil || !strings.Contains(e.Err().Error(), "boom") {
		t.Errorf("Err() = %v, want it to report the panic", e.Err())
	}
	if _, err := e.Submit(t.Context(), operator, newKey(), fundAlice(1)); !errors.Is(err, ErrHalted) {
		t.Errorf("Submit after a panic: error = %v, want ErrHalted", err)
	}
}

func TestRunTwicePanics(t *testing.T) {
	e := New(state.New(), DefaultConfig())
	runInBackground(t, e)
	for !e.started.Load() { // wait until the first Run really owns the engine
		time.Sleep(time.Millisecond)
	}
	defer func() {
		if recover() == nil {
			t.Error("a second Run did not panic; two goroutines would own one state")
		}
	}()
	e.Run()
}
