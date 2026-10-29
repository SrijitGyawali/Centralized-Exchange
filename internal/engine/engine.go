package engine

import (
	"fmt"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/clock"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// Config sizes the engine.
type Config struct {
	// QueueSize is how many admitted requests may wait for the owner
	// goroutine. When the queue is full, Submit fails fast with
	// ErrOverloaded instead of letting memory and latency grow without bound.
	QueueSize int
	// BatchSize bounds commands sharing one durable sync; collection never waits.
	BatchSize int
	// SnapshotDepth is how many price levels per side each published
	// Snapshot holds.
	SnapshotDepth int
	// Journal makes commands durable. nil means no durability (unit tests
	// and benchmarks of the engine alone).
	Journal Journal
	// Hooks are test-only crash points; production leaves them nil.
	Hooks Hooks
	// Observer receives timings and outcomes. nil means none.
	Observer Observer
}

// Journal is what the engine needs from durable storage, and nothing more.
// It is declared here, by its consumer, so tests can substitute a tiny fake.
// Package journal provides the real implementation.
//
// The contract: Append returns nil only after cmd is written AND synced, so
// it survives a crash. Any error means the command may or may not be on
// disk.
type Journal interface {
	Append(cmd command.Command) error
}

// Hooks are failpoints for the crash harness (tests/crash).
type Hooks struct {
	// AfterApply runs after a command is applied and its snapshot
	// published, but BEFORE the reply is sent. A crash here leaves a durable,
	// applied command whose caller never heard back.
	AfterApply func(seq domain.Seq)
}

// DefaultConfig returns sizes suitable for local development.
func DefaultConfig() Config { return Config{QueueSize: 1024, SnapshotDepth: 20, BatchSize: 64} }

// Snapshot is public market data published after every command. Readers get
// it lock-free via Engine.Snapshot.
//
// A Snapshot is IMMUTABLE: the engine builds a fresh one each time and never
// changes it after publishing, and Book is a deep copy that shares no memory
// with the live order book. That is what makes it safe to share one pointer
// between any number of goroutines with no lock. Readers must not modify it
// either.
type Snapshot struct {
	Seq    domain.Seq    // the last command applied when this snapshot was taken
	Book   book.Depth    // top SnapshotDepth levels per side, best first
	Trades []PublicTrade // the most recent fills, newest LAST (at most RecentTrades)
}

// PublicTrade is what everyone may see about a fill: when, at what price,
// how much, and which side was the aggressor. Never who traded, and never
// order IDs: those are private (ADR-0007).
type PublicTrade struct {
	Seq       domain.Seq // the command that produced the fill
	Price     domain.Price
	Qty       domain.Qty
	TakerSide domain.Side // buy = a buyer lifted an ask
}

// RecentTrades is how many fills each Snapshot carries for the trade tape.
const RecentTrades = 50

// request is one item in the intake queue: either a command to sequence and
// apply, or a read-only query to run against the state.
type request struct {
	cmd      command.Command
	reply    chan<- command.Result // buffered (capacity 1): the engine's send never blocks
	query    func(*state.State)    // set for queries; then cmd and reply are unused
	admitted clock.Instant         // when enqueue accepted it; only for measurements
}

// Engine runs the single goroutine that owns all trading state (ADR-0001)
// and turns concurrent requests into one ordered sequence of transitions.
//
// Fields fall into two groups, and the split IS the concurrency design:
//
//   - Owned by the Run goroutine: st, seq, haltedLocal. No other goroutine
//     ever touches them, so they need no lock.
//   - Shared: each has exactly one synchronisation mechanism, named below.
type Engine struct {
	// --- owned by the Run goroutine ---
	st          *state.State
	seq         domain.Seq
	haltedLocal bool
	recent      []PublicTrade // last RecentTrades fills, newest last (owner goroutine only)
	cfg         Config

	// --- shared ---
	intake  chan request             // the queue; closed once, by Close, under mu
	mu      sync.RWMutex             // guards closed: senders hold RLock, Close holds Lock
	closed  bool                     // set by Close; no send may happen after it
	halted  chan struct{}            // closed once, by the Run goroutine, when it halts
	haltErr error                    // written before halted is closed; read only after <-halted
	done    chan struct{}            // closed when Run returns
	started atomic.Bool              // Run may only be started once
	snap    atomic.Pointer[Snapshot] // latest published snapshot
}

// New returns an engine that owns st.
//
// From this call on, st belongs to the engine. The caller must never touch
// it again: the engine's goroutine reads and writes it without locks, so any
// other access would be a data race. Ownership is transferred, not shared.
func New(st *state.State, cfg Config) *Engine {
	def := DefaultConfig()
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = def.QueueSize
	}
	if cfg.SnapshotDepth <= 0 {
		cfg.SnapshotDepth = def.SnapshotDepth
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = def.BatchSize
	}
	if cfg.Observer == nil {
		cfg.Observer = nopObserver{}
	}
	e := &Engine{
		st:     st,
		seq:    st.LastSeq(), // continue after whatever st already applied
		cfg:    cfg,
		intake: make(chan request, cfg.QueueSize),
		halted: make(chan struct{}),
		done:   make(chan struct{}),
	}
	e.publish()
	return e
}

// Run is the owner goroutine's loop. Call it exactly once, usually as
// `go e.Run()`. It handles requests strictly one at a time, in queue order,
// and returns after Close, once every admitted request has been handled.
func (e *Engine) Run() {
	if !e.started.CompareAndSwap(false, true) {
		panic("engine: Run called twice; two goroutines would own the same state")
	}
	defer close(e.done)
	batch := make([]request, 0, e.cfg.BatchSize)
	var pending *request
	for {
		var req request
		if pending != nil {
			req = *pending
			pending = nil
		} else {
			var ok bool
			req, ok = <-e.intake
			if !ok {
				return
			}
		}
		if e.haltedLocal {
			continue
		}
		if req.query != nil {
			e.handleBatch([]request{req})
			continue
		}
		batch = append(batch[:0], req)
	collect:
		for len(batch) < e.cfg.BatchSize {
			select {
			case next, ok := <-e.intake:
				if !ok {
					break collect
				}
				if next.query != nil {
					pending = &next
					break collect
				}
				batch = append(batch, next)
			default:
				break collect
			}
		}
		e.handleBatch(batch)
	}
}

// handleBatch makes the whole collected batch durable before applying any
// command. Legacy Append-only storage is handled one command at a time.
func (e *Engine) handleBatch(batch []request) {
	defer func() {
		if r := recover(); r != nil {
			e.halt(fmt.Errorf("engine: panic at seq %d: %v", e.seq, r))
		}
	}()
	// One clock reading for the whole batch: every request in it stopped
	// waiting at the same moment, when the owner goroutine picked it up.
	obs := e.cfg.Observer
	picked := clock.Now()
	for _, req := range batch {
		obs.QueueWait(picked.Sub(req.admitted))
	}
	if batch[0].query != nil {
		batch[0].query(e.st)
		obs.Query(clock.Since(picked))
		return
	}
	if e.cfg.Journal != nil {
		if writer, ok := e.cfg.Journal.(interface{ AppendBatch([]command.Command) error }); ok {
			cmds := make([]command.Command, len(batch))
			for i := range batch {
				e.seq++
				batch[i].cmd.Seq = e.seq
				cmds[i] = batch[i].cmd
			}
			start := clock.Now()
			if err := writer.AppendBatch(cmds); err != nil {
				e.halt(fmt.Errorf("engine: journal batch ending seq %d: %w", e.seq, err))
				return
			}
			obs.Durable(len(cmds), clock.Since(start))
			for _, req := range batch {
				if !e.apply(req) {
					return
				}
			}
			return
		}
	}
	for _, req := range batch {
		e.seq++
		req.cmd.Seq = e.seq
		if e.cfg.Journal != nil {
			start := clock.Now()
			if err := e.cfg.Journal.Append(req.cmd); err != nil {
				e.halt(fmt.Errorf("engine: journal append seq %d: %w", e.seq, err))
				return
			}
			obs.Durable(1, clock.Since(start))
		}
		if !e.apply(req) {
			return
		}
	}
}

func (e *Engine) apply(req request) bool {
	start := clock.Now()
	fx, err := e.st.Apply(req.cmd)
	if err != nil {
		e.halt(fmt.Errorf("engine: apply seq %d: %w", req.cmd.Seq, err))
		return false
	}
	if !fx.Result.Duplicate { // a retry replays a stored result; its fills already happened
		for _, f := range fx.Result.Fills {
			e.recent = append(e.recent, PublicTrade{Seq: req.cmd.Seq, Price: f.Price, Qty: f.Qty, TakerSide: f.TakerSide})
		}
		if n := len(e.recent); n > RecentTrades {
			e.recent = append(e.recent[:0], e.recent[n-RecentTrades:]...) // keep the newest, reuse the array
		}
	}
	e.publish()
	if h := e.cfg.Hooks.AfterApply; h != nil {
		h(req.cmd.Seq)
	}
	req.reply <- fx.Result
	done := clock.Now()
	e.cfg.Observer.Applied(req.cmd.Body, fx.Result, done.Sub(start))
	e.cfg.Observer.Completed(done.Sub(req.admitted))
	return true
}

// halt stops trading for good (until a restart). Only the Run goroutine
// calls it. haltErr is written BEFORE halted is closed, so any goroutine
// that sees halted closed is guaranteed to see haltErr too: a channel close
// happens-before every receive that observes it (the Go memory model).
func (e *Engine) halt(err error) {
	if e.haltedLocal {
		return
	}
	e.haltedLocal = true
	e.haltErr = err
	close(e.halted)
}

func (e *Engine) publish() {
	// slices.Clone: the snapshot must never share e.recent's array, which the
	// owner goroutine keeps appending to. Shared arrays would be a data race.
	e.snap.Store(&Snapshot{Seq: e.st.LastSeq(), Book: e.st.Depth(e.cfg.SnapshotDepth), Trades: slices.Clone(e.recent)})
}

// Close stops admitting new requests. Requests already admitted are still
// handled; Run returns once they are done (watch Done). Calling Close more
// than once is harmless.
//
// Why the lock: closing a channel while another goroutine sends on it
// panics ("send on closed channel"). Every send happens under mu.RLock with
// a check of closed, and Close takes the full Lock, so no send can overlap
// the close, and none can follow it.
func (e *Engine) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.closed {
		e.closed = true
		close(e.intake)
	}
}

// Done is closed when Run has returned.
func (e *Engine) Done() <-chan struct{} { return e.done }

// QueueLen is how many admitted requests are waiting for the owner goroutine
// right now, and QueueCap how many may wait. Together they are the engine's
// saturation signal: a queue that stays near full means requests are arriving
// faster than the engine can make them durable and apply them. len and cap
// of a channel are safe to read from any goroutine.
func (e *Engine) QueueLen() int { return len(e.intake) }

// QueueCap returns the intake queue's capacity (Config.QueueSize).
func (e *Engine) QueueCap() int { return cap(e.intake) }

// BatchLimit returns the most commands one durable append may carry
// (Config.BatchSize). The config never changes after New, so reading it from
// any goroutine is safe.
func (e *Engine) BatchLimit() int { return e.cfg.BatchSize }

// Err returns why the engine halted, or nil if it is running normally.
func (e *Engine) Err() error {
	select {
	case <-e.halted:
		return e.haltErr
	default:
		return nil
	}
}
