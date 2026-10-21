package engine

import (
	"context"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// There are two ways to read state, for two kinds of data:
//
//	Snapshot(): public market data. Lock-free and instant, possibly one
//	            command behind a request still in flight. Readers never
//	            touch the engine's goroutine at all.
//	Query():    anything, including private data such as balances. Runs ON
//	            the owner goroutine, in queue order with commands, so it
//	            sees every command whose reply came back before it was sent
//	            (read-your-writes), and costs the engine a little time.

// Snapshot returns the latest published market snapshot. It never returns
// nil and never blocks. The value is immutable: share it freely, never
// modify it.
func (e *Engine) Snapshot() *Snapshot { return e.snap.Load() }

// StateReader is the read-only view of the state that queries receive.
//
// It is declared HERE, by the code that uses it, not in package state, which
// is the Go convention: "accept interfaces, defined by the consumer". It
// leaves out Apply, so a query function simply cannot change the state; the
// compiler enforces "queries are read-only". Every method returns copies,
// so nothing a query returns can reach back into live state.
type StateReader interface {
	Balance(user domain.UserID, asset domain.Asset) (available, reserved int64)
	Issued(asset domain.Asset) int64
	Order(id domain.OrderID) (book.Order, bool)
	Depth(n int) book.Depth
	LastSeq() domain.Seq
	CheckInvariants() error
	Digest() [32]byte
	Balances() []state.BalanceRow
}

// A compile-time assertion: this line fails to build if *state.State ever
// stops satisfying StateReader. Go checks interfaces implicitly, so this is
// how you make the intent explicit.
var _ StateReader = (*state.State)(nil)

// Query runs f on the engine's goroutine, in order with commands, and
// returns its result. f must be quick and must return copies (StateReader's
// methods already do).
//
// Query is a generic FUNCTION rather than a method because Go methods cannot
// have their own type parameters. The T lets each caller get back exactly
// the type it computes.
//
// Reads have no effects, so a context that ends while waiting simply means
// "no answer" (ctx.Err()), not "outcome unknown".
func Query[T any](ctx context.Context, e *Engine, f func(StateReader) T) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	out := make(chan T, 1) // capacity 1: the engine never blocks on an abandoned query
	if err := e.enqueue(request{query: func(s *state.State) { out <- f(s) }}); err != nil {
		return zero, err
	}
	select {
	case v := <-out:
		return v, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-e.halted:
		return zero, ErrHalted
	}
}

// Balance is one user's holding of one asset, as of an applied Seq.
type Balance struct {
	Available, Reserved int64
	AsOf                domain.Seq // the last command included in these numbers
}

// Balance reads a user's balance through the owner goroutine.
func (e *Engine) Balance(ctx context.Context, user domain.UserID, asset domain.Asset) (Balance, error) {
	return Query(ctx, e, func(s StateReader) Balance {
		avail, reserved := s.Balance(user, asset)
		return Balance{Available: avail, Reserved: reserved, AsOf: s.LastSeq()}
	})
}
