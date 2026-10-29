package marketdata

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
)

// Config sizes the feed. Zero values take the defaults.
type Config struct {
	// Interval is how often the hub looks for a new snapshot: the feed's
	// maximum update rate (default 100 ms, so at most 10 messages a second).
	Interval time.Duration
	// Buffer is how many messages may wait for one subscriber. A subscriber
	// whose buffer is full is too slow and is disconnected (default 8).
	Buffer int
	// MaxSubscribers bounds the number of open streams (default 1,000).
	MaxSubscribers int
	// WriteTimeout bounds one WebSocket write (default 5 s).
	WriteTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = 100 * time.Millisecond
	}
	if c.Buffer <= 0 {
		c.Buffer = 8
	}
	if c.MaxSubscribers <= 0 {
		c.MaxSubscribers = 1000
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = 5 * time.Second
	}
	return c
}

var (
	// ErrTooManySubscribers: MaxSubscribers streams are already open.
	ErrTooManySubscribers = errors.New("marketdata: too many subscribers")
	// ErrClosed: the hub has shut down.
	ErrClosed = errors.New("marketdata: closed")
)

// Disconnect reasons, sent to the client in the WebSocket close frame.
const (
	ReasonSlow     = "slow consumer: reconnect to get a fresh snapshot"
	ReasonShutdown = "server shutting down"
)

// Hub fans public market data out to any number of subscribers.
//
//	engine.Snapshot() ──(every Interval, only if Seq changed)──► encode ONCE
//	   ──► every subscriber's bounded channel (non-blocking send)
//	          full? → that subscriber is disconnected, nobody else notices
//
// The hub reads the engine's lock-free snapshot pointer and never touches
// the engine's goroutine. However slow or numerous the subscribers are,
// matching cannot be slowed down by them: the property the market-data gate
// requires ("a slow browser never blocks matching").
//
// Why not one shared channel? A Go channel with many receivers DISTRIBUTES
// messages (each goes to one receiver); it does not broadcast. Broadcasting
// needs one channel per subscriber, which is also what lets each one be
// judged and dropped on its own.
type Hub struct {
	cfg    Config
	source func() *engine.Snapshot

	mu      sync.Mutex
	subs    map[*Subscriber]struct{}
	last    []byte     // the latest encoded message: sent first to new subscribers
	lastSeq domain.Seq // the Seq of `last`
	closed  bool
	stats   Stats // running totals, guarded by mu like everything above
}

// Stats are the hub's running totals since it started (exported as metrics).
// Together they answer "is anyone watching, and is anyone too slow?".
type Stats struct {
	Subscribers    int    // streams open right now
	Published      uint64 // snapshots encoded: one per new Seq the hub saw
	Delivered      uint64 // messages placed in subscriber buffers
	DeliveredBytes uint64 // their total size: the feed's outgoing bandwidth
	SlowDrops      uint64 // subscribers disconnected for being too slow
	Refused        uint64 // Subscribe calls turned away (limit reached, or closed)
}

// Subscriber is one open stream.
type Subscriber struct {
	ch     chan []byte
	done   chan struct{}
	reason string // set BEFORE done is closed; read only after <-done
}

// C delivers encoded messages, in order. Each is a complete snapshot.
func (s *Subscriber) C() <-chan []byte { return s.ch }

// Done is closed when the hub drops this subscriber (too slow, or shutdown).
func (s *Subscriber) Done() <-chan struct{} { return s.done }

// Reason says why Done was closed. Call it only after <-Done().
func (s *Subscriber) Reason() string { return s.reason }

// New returns a hub reading snapshots from source (normally
// engine.Snapshot). Call Run to start it.
func New(source func() *engine.Snapshot, cfg Config) *Hub {
	return &Hub{cfg: cfg.withDefaults(), source: source, subs: make(map[*Subscriber]struct{})}
}

// Run publishes until ctx ends, then disconnects every subscriber with
// ReasonShutdown.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(h.cfg.Interval)
	defer t.Stop()
	h.tick() // publish the current state at once
	for {
		select {
		case <-ctx.Done():
			h.shutdown()
			return
		case <-t.C:
			h.tick()
		}
	}
}

// tick publishes the current snapshot if anything changed since the last
// one. Sending only on change keeps an idle market silent, and the ticker
// caps the rate however fast the engine runs.
func (h *Hub) tick() {
	snap := h.source()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed || (h.last != nil && snap.Seq == h.lastSeq) {
		return
	}
	msg := encode(snap) // encoded once, shared by every subscriber (never modified after)
	h.last, h.lastSeq = msg, snap.Seq
	h.stats.Published++
	for s := range h.subs {
		select {
		case s.ch <- msg:
			h.stats.Delivered++
			h.stats.DeliveredBytes += uint64(len(msg))
		default: // buffer full: this subscriber cannot keep up
			h.drop(s, ReasonSlow)
			h.stats.SlowDrops++
		}
	}
}

// Subscribe opens a stream. The newest snapshot, if there is one, is
// already waiting in it, so a new viewer sees the book immediately instead
// of waiting for the next trade.
func (h *Hub) Subscribe() (*Subscriber, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		h.stats.Refused++
		return nil, ErrClosed
	}
	if len(h.subs) >= h.cfg.MaxSubscribers {
		h.stats.Refused++
		return nil, ErrTooManySubscribers
	}
	s := &Subscriber{ch: make(chan []byte, h.cfg.Buffer), done: make(chan struct{})}
	if h.last != nil {
		s.ch <- h.last // cannot block: the buffer is new and has room
		h.stats.Delivered++
		h.stats.DeliveredBytes += uint64(len(h.last))
	}
	h.subs[s] = struct{}{}
	return s, nil
}

// Unsubscribe removes s. Calling it for an already-dropped subscriber is
// harmless.
func (h *Hub) Unsubscribe(s *Subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subs, s)
}

// Stats returns a copy of the running totals.
func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.stats
	s.Subscribers = len(h.subs)
	return s
}

// Subscribers returns how many streams are open.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// drop disconnects s. Caller holds h.mu. reason is written BEFORE done is
// closed, so whoever sees done closed also sees the reason (a channel close
// happens-before every receive that observes it).
func (h *Hub) drop(s *Subscriber, reason string) {
	if _, ok := h.subs[s]; !ok {
		return
	}
	delete(h.subs, s)
	s.reason = reason
	close(s.done)
}

func (h *Hub) shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		h.drop(s, ReasonShutdown)
	}
}
