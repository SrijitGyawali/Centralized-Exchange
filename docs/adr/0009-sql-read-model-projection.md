# ADR-0009: The SQL read model: a journal-following projector

- **Status:** Accepted
- **Date:** 2026-10-26

## Context

The engine holds only *current* state in memory: open orders, balances,
dedupe results. Users and operators also need *history*: "my last 100
trades", "every ledger line for user 7", "which orders were canceled". That
belongs in SQL (ADR-0003: PostgreSQL, pgx, tern).

The danger is a read model that silently disagrees with the engine, for
example by missing an event, double-counting one after a crash, or getting
"fixed" by hand. It must also never slow trading down or become something
trading depends on.

## Options considered

1. **The engine writes SQL inline** before replying. Every order would wait
   for PostgreSQL, a database outage would stop trading, and two
   durability systems would have to agree. ❌
2. **An in-memory event channel from the engine to a SQL writer.**
   Messages are lost on crash or overflow (plan §12), and the writer can't
   rebuild what it missed. ❌
3. **A projector that follows the durable journal and replays it through
   its own copy of the state machine** ✅. The journal is already the
   source of truth, `Apply` is deterministic, and the effects (results and
   ledger journals) carry everything SQL needs.
4. **Checkpoint storage:** in a file vs **in the same database, updated in
   the same transaction** ✅. A file checkpoint can disagree with the rows
   after a crash; the same transaction can't.

## Decision

### Data flow
- The projector opens `journal.wal` **read-only** and reads complete frames
  from a remembered byte offset (`journal.ScanFrom`). An incomplete last
  frame means the engine is mid-append, so it's read again later.
- Each command is applied to the projector's **private** `state.State`, and
  `plan()` turns `(command, effects)` into SQL statements. That's pure and
  unit-tested.

### Transactions and checkpoint
- Up to `BatchSize` (256) commands go into **one transaction**:
  1. `SELECT seq FROM projection_checkpoint FOR UPDATE`. It must equal the
     Seq before this batch, otherwise another writer is active: refuse and
     write nothing.
  2. All rows, sent as one `pgx.Batch` round trip.
  3. `UPDATE projection_checkpoint SET seq = last`.
  4. `COMMIT`.
- Crash anywhere → the database is exactly at the last committed
  checkpoint. On restart, `New` replays journal commands `1..checkpoint`
  into a fresh state *without writing*, then continues. Primary keys on
  journal identity (`seq`, `seq+fill_index`, `seq+journal_index+line_index`)
  turn any double write into an error instead of double money.
- If a transaction fails, the replay state is ahead of the database, so
  that projector is discarded. `Run` rebuilds from the checkpoint after a
  backoff.
- A database built from a **different journal** is refused, and the
  projector says to drop the database and rebuild it:
  - its checkpoint is *ahead* of the journal; or
  - the journal's command at the checkpoint Seq has a different user or key
    from the one the checkpoint row recorded (`last_user_id`, `last_key`).
    This catches a replaced journal of the same or greater length. It was
    found while smoke-testing with a reused local database.

### Schema (migration 001)
`commands`, `orders` (full lifecycle: open, partially_filled, filled,
canceled, canceled_ioc, canceled_self_trade), `trades`, `ledger_journals`,
`ledger_lines`, `account_balances`, `projection_checkpoint`. Money is
`BIGINT` smallest units; enumerations are checked text. Migrations are
embedded in the binary and applied on every start; applied migrations are
never edited.

### Independence from trading
- The engine never reads SQL and never waits for the projector.
- `GET /v1/status` shows `applied_seq`, `projected_seq` and `lag`. Lag is
  normal; growing lag or `healthy: false` means stale history, not
  stopped trading.
- Readiness (`/readyz`) does **not** depend on PostgreSQL.

### Verification
- `Reconcile` compares SQL balances (including issuance) with the replay
  state **at the same Seq**, and checks that each asset's ledger sums to
  zero.

## Consequences

- **Easier:** history and audit queries in plain SQL; the database is
  disposable and can be rebuilt identically; outages are harmless to
  trading.
- **Harder:** the projector repeats the engine's CPU work (a second
  `Apply` per command); rebuilds replay the whole journal (Tier 2
  snapshots shorten that); history is eventually consistent, so clients
  must read `projected_seq` / `lag`.
- **Failure model:** like the journal (ADR-0006), process crashes.
  Under *power loss*, the projector could have read a frame that was in
  the OS cache but never fsynced, so SQL could be briefly ahead of a
  recovered journal. `New` detects that ("checkpoint ahead of the
  journal") and refuses; rebuilding the database fixes it.
- **Dependencies:** pgx and tern in production code; testcontainers-go
  (a large dependency tree) **only in tests**, behind the `integration`
  build tag.
- **Evidence:** `plan` unit tests; integration tests against real
  PostgreSQL 17 (match vs engine, growing journal, crash before COMMIT,
  identical rebuild with different batch sizes, incomplete last frame,
  checkpoint ahead refused, second writer refused); and the end-to-end
  outage test (`docker pause` the database: trading continues, lag grows,
  then catches up exactly once).
