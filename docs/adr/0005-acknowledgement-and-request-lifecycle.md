# ADR-0005: Acknowledgement and the request lifecycle

- **Status:** Accepted
- **Date:** 2026-10-20

## Context

ADR-0001 put all state behind one owner goroutine. Many goroutines (HTTP
handlers, later WebSocket readers) must now talk to it. Every caller needs
an exact answer to one question: **did my command happen?** Networks drop
responses, clients time out, queues fill up, and processes shut down. If
the answers aren't precise, users double-trade or wrongly believe a trade
failed.

## Options considered

1. **A mutex around State**, with handlers calling Apply themselves.
   Works, but the order of commands is decided by lock acquisition (not
   recorded), slow handlers block trading, and it contradicts ADR-0001. ❌
2. **An unbounded queue.** Never refuses work, but memory and latency grow
   without limit under overload. ❌
3. **A bounded queue to one owner goroutine, with non-blocking admission**
   ✅. Gives explicit overload and one recorded order.
4. **Cancel commands when the client cancels.** That's impossible to do
   honestly once a command may already have executed. ❌ Cancellation only
   stops the *wait*.

## Decision

### Admission draws the line

| When it fails | Errors | Did the command run? |
|---|---|---|
| **Before admission** | `ErrInvalidCommand`, `ErrOverloaded`, `ErrClosed`, `ErrHalted`, a context already done | **No, definitely** |
| **After admission, no reply** | `ErrOutcomeUnknown` (wrapping the context error or `ErrHalted`) | **Maybe**. Retry with the same idempotency key. |
| **Reply received** | `nil` error + `Result` (accepted *or* a business rejection) | **Yes**, and the Result says how |

- An admitted command **runs**, whatever the caller does afterwards.
  Request cancellation is not rollback.
- When the reply and a cancellation arrive at once, Go's `select` picks
  randomly, so `Submit` checks once more for the reply. A known outcome
  always beats "unknown".

### Ordering
- **Seq is assigned when the owner dequeues.** That's the fairness point.
  Concurrent senders have no promised wall-clock order; the Seq *is* the
  order, and replay uses it.
- Every command consumes a Seq; queries don't.

### Acknowledgement
- **Today (week 3):** a reply means *applied in memory*.
- **From week 4:** the owner appends to the WAL and fsyncs **before**
  Apply, so a reply means *durable and applied*. This ADR's lifecycle
  doesn't change; only what "reply" guarantees gets stronger.

### No goroutine leaks
- Reply channels have **capacity 1**, and the owner sends exactly once. A
  buffered send never blocks, so an abandoned caller can't stall trading.
  The orphaned result is simply garbage-collected.

### Reads
- **Snapshot** (public market data): an immutable value behind an
  `atomic.Pointer`, republished after every command *before* the reply,
  so a caller holding a result sees it reflected. Lock-free.
- **Query** (private data): runs on the owner, in order with commands, so
  it has read-your-writes semantics. Receives a read-only `StateReader`
  interface, so it can't call Apply.

### Overload, shutdown, halting
- Queue size is configurable (default 1024). When it's full, the request
  is refused immediately and the API answers 503 (week 5).
- **Close** stops admission and **drains** everything admitted; `Done`
  signals completion. A mutex makes closing the channel safe while senders
  run, because sending on a closed channel panics.
- **Halt:** if `Apply` returns an error or panics, the engine stops
  trading permanently and `Err()` explains why. Requests already queued
  are dropped (their callers get `ErrOutcomeUnknown`), and nothing new is
  admitted. Readiness will report this (week 5). Recovery is an operator
  restart, and from week 4, journal replay.

## Consequences

- **Easier:** one recorded order; overload is visible instead of slow;
  clients get precise, documented semantics; no locks in the core.
- **Harder:** every command pays a goroutine handoff, measured at about
  1.7 µs on the dev laptop (evidence/bench/2026-10-20-engine-overhead.txt).
  Clients must handle `ErrOutcomeUnknown` by retrying with the same key.
- **Evidence:** 14 engine tests run under `-race`, including 100
  goroutines sending one key at once (exactly one effect), 50 racing one
  key with different requests (one winner, 49 conflicts), 8 traders plus 4
  readers (gap-free Seqs, invariants and money conserved), the
  lost-response retry, overload, drain-on-close, and halting on an error
  and on a panic. A deliberately racy test was confirmed to fail under
  `-race`.
