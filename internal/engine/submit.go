package engine

import (
	"context"
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// Submit sends one command to the engine and waits for its result. It is
// safe to call from any number of goroutines at once.
//
// A request's life, and what each error means (ADR-0005):
//
//  1. Checks before admission: a nil body, a context that is already done,
//     a closed, halted or full engine. These return an error and the
//     command definitely did NOT run.
//  2. Admission: the request enters the bounded queue. From here on it WILL
//     be executed (unless the engine halts), whatever the caller does.
//  3. The owner goroutine assigns its Seq, applies it, publishes a new
//     snapshot and puts the result on this request's reply channel.
//  4. The caller receives the result: accepted, or a business rejection
//     such as insufficient funds. Both are a nil error.
//
// If the caller's context ends or the engine halts during step 2 or 3,
// Submit returns ErrOutcomeUnknown. The command may or may not have run.
// Canceling a context NEVER cancels or rolls back a trade; it only stops
// this caller from waiting. To find out what happened, retry with the same
// idempotency key: if it ran, the retry returns its stored result.
func (e *Engine) Submit(ctx context.Context, user domain.UserID, key string, body command.Body) (command.Result, error) {
	if body == nil {
		return command.Result{}, ErrInvalidCommand
	}
	if err := ctx.Err(); err != nil {
		return command.Result{}, err // not admitted: plainly failed
	}

	// Capacity 1 is the whole goroutine-leak story: the engine sends exactly
	// once, and a buffered send never blocks, even if this caller has
	// already given up and nobody will ever receive.
	reply := make(chan command.Result, 1)
	if err := e.enqueue(request{cmd: command.Command{User: user, Key: key, Body: body}, reply: reply}); err != nil {
		return command.Result{}, err
	}

	// Admitted. Wait for the first of: the result, the caller giving up,
	// or the engine halting.
	select {
	case res := <-reply:
		return res, nil
	case <-ctx.Done():
		return lastChance(reply, ctx.Err())
	case <-e.halted:
		return lastChance(reply, ErrHalted)
	}
}

// lastChance handles a tie. When several select cases are ready at once,
// Go picks one at RANDOM, so the result may have arrived at the same moment
// as the cancellation. If it did, return it: a known outcome always beats
// "unknown".
func lastChance(reply <-chan command.Result, cause error) (command.Result, error) {
	select {
	case res := <-reply:
		return res, nil
	default:
		return command.Result{}, fmt.Errorf("%w: %w", ErrOutcomeUnknown, cause)
	}
}

// enqueue admits a request without ever blocking: it either enters the
// queue now or is refused now.
//
// The read lock is what makes Close safe. Sending on a closed channel
// panics, so every send happens while holding mu.RLock after checking
// closed, and Close needs the full lock to close the channel. Many senders
// can hold the read lock together; the send itself never blocks (select with
// default), so holding the lock across it cannot deadlock.
func (e *Engine) enqueue(req request) error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		return ErrClosed
	}
	select {
	case <-e.halted:
		return ErrHalted
	default:
	}
	select {
	case e.intake <- req:
		return nil
	default:
		return ErrOverloaded
	}
}
