# ADR-0001: Project scope and a single global state owner

- **Status:** Accepted
- **Date:** 2026-10-01

## Context

We are building a spot-exchange simulator to learn Go and backend systems
engineering. Every shared resource in an exchange is shared money: a user's
USDT balance funds orders on every symbol. If two goroutines could change
balances or books concurrently, every transition would need locks plus a
protocol that joins reservation, matching, settlement and release. If a
crash interrupted that protocol, funds could be stranded or duplicated.

Constraints: solo developer, 8 weeks to an interview-ready release, and
correctness and recoverability matter more than raw throughput.

## Options considered

1. **Global in-memory state machine + file WAL** (recommended by the plan).
   One goroutine owns all books and balances. Commands are sequenced,
   journaled, then applied by a pure function. Pros: atomic transitions,
   trivial replay, and it teaches storage and determinism. Cons: one CPU
   core is the throughput ceiling, and we own WAL recovery.
2. **PostgreSQL-centred transactional service.** Pros: mature durability,
   SQL skills. Cons: matching must still be serialised deliberately, and
   commit uncertainty must be handled.
3. **Independent account and symbol services.** Pros: horizontal scale.
   Cons: needs durable messaging, transfer state machines and cross-owner
   recovery. That is a separate project.

## Decision

- **Scope (Tier 1):** simulated spot trading only. One symbol, BTC/USDT.
  GTC limit, IOC limit, cancel. Zero fees. Funding through explicit test
  commands. No real custody.
- **Ownership:** one engine goroutine owns *all* mutable trading state:
  books, accounts, reservations, ledger, dedupe records, ID counters and
  configuration. No other goroutine reads these objects. Readers receive
  immutable copies tagged with an applied sequence.
- **Transition:** `state.Apply(cmd) Result` is deterministic and performs no
  I/O, clock or random calls. Time comes from the command (stamped by the
  engine at sequencing).
- **Authority:** the WAL plus compatible replay logic is the source of
  truth. PostgreSQL is a rebuildable projection.
- **Adding symbols:** more books inside the same `State`, sharing the same
  accounts and the same global sequence. No additional writers.

## Consequences

- Easier: atomicity (one function call is one transition), replay, the
  explain/inspector feature, and reasoning about concurrency.
- Harder: throughput is capped by one goroutine plus storage latency. We
  will *measure* that ceiling (week 7) instead of assuming it.
- Terminology: we say "single-writer state machine", never "lock-free".
- Evidence: invariant tests after every generated command; replaying a
  fixture twice gives identical state digests; `go test -race` exercises
  concurrent submissions and reads.
