// Package engine runs the single goroutine that owns all trading state and
// turns concurrent requests into one ordered sequence of transitions
// (ADR-0001), with the request lifecycle defined in ADR-0005.
//
//	many goroutines                  ONE owner goroutine (Run)
//	───────────────                  ─────────────────────────
//	Submit(ctx, …) ─┐                for req := range intake {
//	Submit(ctx, …) ─┼─► bounded ──►      Seq++                 ← fairness point
//	Query(ctx, …)  ─┘   intake           state.Apply(cmd)      ← pure transition
//	                    (FIFO)           publish Snapshot      ← atomic pointer
//	                                     reply (1-slot chan)
//	Snapshot() ◄──── lock-free read  }
//
// Week 4 inserts "append to the WAL and fsync" between Seq++ and Apply,
// which makes every reply mean durable AND applied.
//
// # Concurrency design
//
//   - Ownership replaces locking. Only the Run goroutine reads or writes the
//     State, so State has no mutex. New takes ownership: after New, nobody
//     else may touch the State.
//   - Admission is non-blocking and bounded: a full queue returns
//     ErrOverloaded immediately (HTTP 503) instead of growing memory.
//   - Seq is assigned when the owner DEQUEUES a request. That is the
//     documented fairness point; concurrent senders have no promised
//     wall-clock order.
//   - Reply channels have capacity 1: the owner's single send never blocks,
//     so a caller that gave up can never stall trading or leak a goroutine.
//   - Canceling a request's context only stops the CALLER waiting. An
//     admitted command still runs; its caller gets ErrOutcomeUnknown and
//     resolves it by retrying with the same idempotency key.
//   - Reads: Snapshot is lock-free (immutable value behind an atomic
//     pointer); Query runs on the owner, in order with commands, through
//     the read-only StateReader interface.
//   - Close stops admission and drains what was admitted. A mutex makes
//     closing the queue safe while other goroutines may be sending.
//   - Halting: if Apply fails or panics, the engine stops trading for good
//     (Err says why). It never guesses its way past a broken invariant.
package engine
