package engine

import "errors"

// The engine's errors fall into two groups, and the difference matters to
// every caller (ADR-0005):
//
//	NOT ADMITTED: the command never entered the queue, so it definitely
//	did NOT happen. Safe to report as a plain failure.
//	    ErrInvalidCommand, ErrOverloaded, ErrClosed, ErrHalted (from Submit
//	    before admission), and a context already done before Submit.
//
//	ADMITTED, NO ANSWER: the command was queued but its result never came
//	back to this caller. It MAY have happened. The only safe move is to
//	retry with the SAME idempotency key, which returns the real result if
//	it ran.
//	    ErrOutcomeUnknown (wrapping the cause: the caller's context, or
//	    ErrHalted).
var (
	// ErrInvalidCommand: the request is malformed before it reaches the
	// engine (for example, no body). Not admitted.
	ErrInvalidCommand = errors.New("engine: invalid command")

	// ErrOverloaded: the intake queue is full. Not admitted. The API will
	// answer 503 so the client backs off and retries later.
	ErrOverloaded = errors.New("engine: overloaded, intake queue full")

	// ErrClosed: the engine is shutting down and admits nothing new.
	ErrClosed = errors.New("engine: closed")

	// ErrHalted: the engine stopped itself after an internal error (a
	// failed Apply or a panic) and admits nothing new. Engine.Err says why.
	// Trading stays stopped until an operator restarts it.
	ErrHalted = errors.New("engine: halted")

	// ErrOutcomeUnknown: the command was admitted, but this caller stopped
	// waiting (timeout, cancellation) or the engine halted before
	// answering. Retry with the same key to learn what happened.
	ErrOutcomeUnknown = errors.New("engine: outcome unknown")
)
