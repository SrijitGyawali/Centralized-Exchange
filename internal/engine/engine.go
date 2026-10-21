package engine

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
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
	// SnapshotDepth is how many price levels per side each published
	// Snapshot holds.
	SnapshotDepth int
	// Journal makes commands durable. nil means no durability (unit tests
	// and benchmarks of the engine alone).
	Journal Journal
	// Hooks are test-only crash points; production leaves them nil.
	Hooks Hooks
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
func DefaultConfig() Config { return Config{QueueSize: 1024, SnapshotDepth: 20} }

// Snapshot is public market data published after every command. Readers get
// it lock-free via Engine.Snapshot.
//
// A Snapshot is IMMUTABLE: the engine builds a fresh one each time and never
// changes it after publishing, and Book is a deep copy that shares no memory
// with the live order book. That is what makes it safe to share one pointer
// between any number of goroutines with no lock. Readers must not modify it
// either.
type Snapshot struct {
	Seq  domain.Seq // the last command applied when this snapshot was taken
	Book book.Depth // top SnapshotDepth levels per side, best first
}

// request is one item in the intake queue: either a command to sequence and
// apply, or a read-only query to run against the state.
type request struct {
	cmd   command.Command
	reply chan<- command.Result // buffered (capacity 1): the engine's send never blocks
	query func(*state.State)    // set for queries; then cmd and reply are unused
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
	for req := range e.intake { // ends when Close closes intake AND it is drained
		if e.haltedLocal {
			continue // halted: drop it; its caller learns via ErrHalted
		}
		e.handle(req)
	}
}

// handle runs one request. The deferred recover turns a panic anywhere in
// Apply (a "cannot happen" guard firing) into a halt: the state may be
// half-changed, so the engine stops instead of continuing on wrong money.
// The process stays up, so the error can be read and reported.
func (e *Engine) handle(req request) {
	defer func() {
		if r := recover(); r != nil {
			e.halt(fmt.Errorf("engine: panic at seq %d: %v", e.seq, r))
		}
	}()

	if req.query != nil {
		req.query(e.st)
		return
	}

	e.seq++
	req.cmd.Seq = e.seq // the fairness point: order of dequeue = order of Seq

	// Durability BEFORE effect: the command is written and fsynced before
	// it changes anything, so a reply always means "durable AND applied".
	// If the append fails, the command may be partly or fully on disk, and
	// the engine cannot know. It halts; a restart re-reads the journal and
	// finds out.
	if e.cfg.Journal != nil {
		if err := e.cfg.Journal.Append(req.cmd); err != nil {
			e.halt(fmt.Errorf("engine: journal append seq %d: %w", e.seq, err))
			return
		}
	}

	fx, err := e.st.Apply(req.cmd)
	if err != nil {
		e.halt(fmt.Errorf("engine: apply seq %d: %w", e.seq, err))
		return
	}
	// Publish BEFORE replying: by the time a caller holds its result, the
	// snapshot already includes it (read-your-writes for market data).
	e.publish()
	if h := e.cfg.Hooks.AfterApply; h != nil {
		h(e.seq)
	}
	req.reply <- fx.Result // never blocks: capacity 1, exactly one send
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
	e.snap.Store(&Snapshot{Seq: e.st.LastSeq(), Book: e.st.Depth(e.cfg.SnapshotDepth)})
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

// Err returns why the engine halted, or nil if it is running normally.
func (e *Engine) Err() error {
	select {
	case <-e.halted:
		return e.haltErr
	default:
		return nil
	}
}
