// Package engine runs the single goroutine that owns all trading state and
// turns concurrent requests into one ordered sequence of transitions
// (ADR-0001), with the request lifecycle defined in ADR-0005.
//
//	many goroutines                  ONE owner goroutine (Run)
//	───────────────                  ─────────────────────────
//	Submit(ctx, …) ─┐                for req := range intake {
//	Submit(ctx, …) ─┼─► bounded ──►      Seq++                 ← fairness point
//	Query(ctx, …)  ─┘   intake           Journal.Append        ← durability point (write + fsync)
//	                    (FIFO)           state.Apply(cmd)      ← pure transition
//	                                     publish Snapshot      ← atomic pointer
//	                                     reply (1-slot chan)
//	Snapshot() ◄──── lock-free read  }
//
// Because the journal append comes BEFORE Apply, every reply means durable
// AND applied. If the append fails, the engine halts: the command may or may
// not be on disk, and only a restart that re-reads the journal can tell.
// The Journal interface is declared here (one method), so tests use a fake;
// package journal provides the real one, and package node wires them.
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
