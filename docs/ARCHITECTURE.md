# Architecture and project structure

This document describes how the backend is organised: the main components,
how a request travels through them, where data is stored, and which
directory or package owns each responsibility. It explains the *why* behind
the repository layout. Detailed decisions live in [ADRs](adr/), and the full
roadmap is in the [master plan](plan/CEX_Go_Guided_Master_Plan.md).

> Status: Day 1 skeleton. Most packages contain only a `doc.go` that states
> what they will own. Each section notes the week it gets implemented.

---

## 1. What we are building

A **deterministic spot-exchange simulator** for paper trading:

- one process, one trading pair (BTC/USDT) in Tier 1
- price-time priority matching (GTC limit, IOC limit, cancel)
- exact integer accounting with a double-entry ledger
- a durable command journal (WAL) that can rebuild the full state after a crash
- idempotent requests, so a retried request never applies twice
- a replay inspector that explains any order from request to ledger postings

It never holds real funds. "Production-ready" means ready for a documented
paper-trading environment with known failure limits.

---

## 2. The big picture

```
                         ┌───────────────────────── cexd process ─────────────────────────┐
                         │                                                                │
 HTTP client ──► internal/api ──► bounded intake ──► internal/engine (the ONE owner)      │
 (REST, JSON)    validate, auth,   (buffered chan,    1. assign sequence + timestamp       │
                 parse decimals    reject when full)  2. internal/journal: append + Sync ──┼──► data/wal/*.wal
                         ▲                            3. internal/state.Apply (pure)       │      (authoritative)
                         │                            4. reply on a 1-slot channel         │
                         │                                     │                           │
                         └──── reply / result ◄────────────────┘                           │
                                                               │ immutable snapshots       │
 WebSocket client ◄── internal/marketdata ◄────────────────────┘ (copies tagged with seq)  │
                                                                                           │
                         internal/projector ◄── tails the WAL, replays it, writes rows ────┼──► PostgreSQL
                         (separate goroutine, own replay state, transactional checkpoint)  │    (read model only,
                         └─────────────────────────────────────────────────────────────────┘     rebuildable)

 cmd/replay (offline CLI) ── reads data/wal/*.wal ── state.Apply ── prints state / explains an order
```

Key ideas:

1. **One owner of mutable state.** Only the engine goroutine touches books,
   balances, reservations and dedupe records. No other goroutine reads them,
   so the core needs no application-level mutex. We call this a
   *single-writer state machine*. It is not "lock-free" in the formal
   sense, because Go channels synchronise internally.
2. **Durable before applied before acknowledged.** A command is written to
   the WAL and `fsync`ed, then applied, then answered. A success response
   therefore always means the effect survives a crash.
3. **`Apply` is pure.** It has no disk, network, clock or randomness. The
   same WAL replayed twice produces the same state, byte for byte. Replay,
   recovery, the projector and the inspector all rest on this property.
4. **The WAL is the truth. PostgreSQL is a view.** You can drop the
   database and rebuild it from the log. Risk checks and dedupe never
   consult SQL.

---

## 3. Components

| Component | Package | Responsibility | Week |
|---|---|---|---|
| Units and rules | `internal/domain` | `Price`, `Qty`, `Notional` integer types, checked arithmetic, decimal-string parsing, symbol config, `Side`, `TimeInForce`, IDs | 1 |
| Commands | `internal/command` | Data-only command structs (`PlaceOrder`, `CancelOrder`, `Fund`) and their `Result`s. Shared by api, engine, journal and state. | 1-3 |
| Order book | `internal/book` | Sorted price levels, FIFO queue per level, order-ID index, matching loop, depth copies | 1-2 |
| State machine | `internal/state` | `State` (books, accounts, reservations, ledger, dedupe, config) and the pure `Apply(cmd) Result` transition | 3 |
| Journal (WAL) | `internal/journal` | Frame encoding (magic, version, length, seq, payload, checksum), append + `Sync`, recovery reader, corruption handling | 4 |
| Engine | `internal/engine` | Owner goroutine: intake queue, sequencing, group commit, calls `Apply`, delivers replies, publishes read snapshots, halts on storage errors | 3-5 |
| HTTP API | `internal/api` | Routing, JSON validation, auth, ownership checks, rate limits, status-code mapping, health/readiness | 1 (health), 5 |
| Market data | `internal/marketdata` | Sequenced depth snapshots, WebSocket fan-out, slow-consumer disconnect | 6 |
| Projector | `internal/projector` | Tails the WAL, writes orders/trades/ledger rows plus checkpoint in one SQL transaction | 6 |
| Config | `internal/config` | Reads environment variables into a typed, validated `Config` | 1 |
| Server binary | `cmd/cexd` | Composition root: builds every component, wires them, runs, shuts down gracefully | 1 |
| Replay CLI | `cmd/replay` | Offline replay to a chosen sequence, state digest, "explain order" | 4, 8 |

Not built yet, added when needed: `internal/telemetry` (Prometheus metrics,
week 7), `migrations/` (SQL schema, week 6), `web/` (frontend, week 6).

---

## 4. Life of one order

1. **api** authenticates the caller, rejects bodies that are oversized or
   malformed or that have unknown fields, and parses `"60000"` / `"0.1"`
   into checked integers. Invalid transport requests never enter the
   journal.
2. **api** builds a data-only `command.PlaceOrder` and tries a non-blocking
   send into the bounded intake channel. If the channel is full it returns
   `503`, so overload is rejected *before* admission.
3. **engine** takes the next command and assigns `seq` and the server
   timestamp. This is the fairness point, so FIFO priority is defined here
   and not by network arrival.
4. **engine** appends a batch to the **journal** and calls `Sync`. If that
   fails, it stops trading. It never applies or acknowledges an unsynced
   command.
5. **state.Apply**, in sequence order: dedupe check (user + key + request
   hash), then validation, risk and reservation, matching, settlement and
   ledger postings, all as one transition. A rejected command leaves no
   partial effect.
6. **engine** sends the `Result` on the request's buffered reply channel.
   **api** writes the HTTP response. If the client already gave up, the
   trade still happened, and the client recovers by retrying with the same
   key.
7. **marketdata** and **projector** observe the new state or log on their
   own schedule. A slow consumer never blocks matching.

---

## 5. Where data lives

| Data | Where | Authoritative? | Survives restart via |
|---|---|---|---|
| Commands (every accepted request, including rejections) | `data/wal/NNNNNNNN.wal` (append-only files) | **Yes** | It *is* the durable record |
| Books, balances, reservations, dedupe results, ID counters | Memory, inside `state.State` | Derived | Replaying the WAL (Tier 2: snapshot + WAL suffix) |
| Snapshots (Tier 2) | `data/snapshots/` | Derived, a recovery shortcut | Checksummed and atomically published |
| Orders, trades, ledger lines, balances for queries | PostgreSQL tables | **No**, a projection | Rebuild from WAL; `projection_checkpoint` records progress |
| Public market data | Memory, pushed over WebSocket | No | Client resubscribes and fetches a fresh snapshot |

Money representation (see [ADR-0002](adr/0002-units-and-fixed-point.md)):
all amounts are `int64` counts of the smallest unit (lots, ticks, atoms). We
never use floats, and every multiplication and addition is overflow-checked.

Planned PostgreSQL tables (week 6): `orders`, `trades`, `ledger_journals`,
`ledger_lines`, `account_balances`, `processed_events`,
`projection_checkpoint`.

---

## 6. Repository layout

```
.
├── cmd/                      # One sub-directory per binary. Each has a main package.
│   ├── cexd/                 #   The exchange server (composition root only, no business logic)
│   └── replay/               #   Offline replay / inspector CLI
├── internal/                 # Private code. The Go toolchain forbids imports from other modules.
│   ├── domain/               #   Pure: units, checked math, enums, IDs
│   ├── command/              #   Pure: command and result data types
│   ├── book/                 #   Pure: order book + matching
│   ├── state/                #   Pure: full state + Apply
│   ├── journal/              #   I/O:  WAL files
│   ├── engine/               #   Concurrency: the single owner goroutine
│   ├── api/                  #   I/O:  HTTP handlers
│   ├── marketdata/           #   I/O:  WebSocket feeds
│   ├── projector/            #   I/O:  PostgreSQL read model
│   └── config/               #   Startup configuration
├── tests/                    # Cross-package tests: crash harness, end-to-end, recovery
├── loadtest/                 # k6 / Go workload definitions (week 7)
├── deploy/                   # Dockerfile, docker-compose, env templates (week 7)
├── labs/                     # Small Go runtime experiments (scheduler, GC, escape analysis…)
├── evidence/                 # Benchmark output, profiles, crash reports, demo recordings
├── track/                    # Day-by-day notes (track/2026-10-01/…): what, why, how
├── docs/
│   ├── ARCHITECTURE.md       #   This file
│   ├── adr/                  #   Architecture Decision Records, one decision per file
│   └── plan/                 #   The master plan
├── .github/workflows/        # CI: fmt, vet, build, race-enabled tests
├── Makefile                  # Short aliases for the commands you run daily
└── go.mod                    # Module path + pinned Go version
```

### Why this shape (Go conventions)

- **`cmd/<name>/main.go`**: the standard place for binaries. `main` stays
  thin and wires things together. Logic in `main` cannot be imported or
  unit-tested.
- **`internal/`**: the compiler enforces that only code inside this module
  can import these packages. That keeps the API surface private, so we can
  refactor freely. We deliberately have **no `pkg/` directory** because we
  don't publish a library.
- **Packages are named for what they provide** (`book`, `journal`), not
  for a technical layer (`models`, `utils`, `services`). Call sites read
  naturally: `book.New()`, `journal.Open()`.
- **Tests live next to code** (`book.go` with `book_test.go`). Package-level
  fixtures live in that package's `testdata/` directory, which the go tool
  ignores when building.
- **`doc.go`** holds the package comment, which `go doc` shows. Every
  package starts with one that states its responsibility and its boundaries.
- **Interfaces are declared by the consumer, at I/O seams only.** For
  example, `engine` declares the small `Journal` interface it needs, and
  `journal` provides a concrete struct. Pure packages use concrete types.

### Dependency rule (import direction)

```
 cmd/cexd ──► api ──► engine ──► state ──► book ──► domain
                │        │         │                  ▲
                │        └──► journal ──► command ─────┤
                └──► config         marketdata, projector ──► state/command/domain
```

- `domain`, `command`, `book` and `state` form the **pure core**. They
  import only the standard library and each other. They never import
  `journal`, `api`, `engine`, `database/sql`, `net/http`, `os` or `time.Now`.
- I/O packages depend on the core, never the other way round.
- Cycles are compile errors in Go, so a bad dependency shows up immediately.

---

## 7. HTTP API (Tier 1 target, week 5)

| Method & path | Purpose | Notes |
|---|---|---|
| `GET /healthz` | Liveness: the process is running | Day 1 |
| `GET /readyz` | Readiness: replay done, journal writable, trading enabled | Day 1 (always 503 until the engine exists) |
| `POST /v1/orders` | Place an order | Body: `symbol`, `side`, `type`, `time_in_force`, `price`, `quantity` (strings), `client_key`. Returns order ID, status, `applied_seq` |
| `DELETE /v1/orders/{id}` | Cancel (sequenced command) | Requires a request key, owner only |
| `GET /v1/orders/{id}` | Order status | Owner only |
| `GET /v1/requests/{client_key}` | Result of an earlier request | Lost-response recovery |
| `GET /v1/balances` | Available and reserved per asset | Tagged `as_of_seq` |
| `GET /v1/book?symbol=BTC-USDT` | Depth snapshot | Tagged `as_of_seq` |
| `GET /v1/stream` (WebSocket) | Depth snapshots, later incremental updates | Week 6 |

Status codes: `400` malformed, `401`/`403` auth, `409` same key with a
different payload, `429` rate limited, `503` overloaded before admission.
A *business* rejection (for example insufficient funds) is a durable,
typed result and not an HTTP error. Money and large IDs are JSON
**strings**.

---

## 8. Build order

Feature order follows the plan: correct state transitions, then durable
ordering, retry safety, API, visibility, safeguards, and finally
replication.

| Week | Packages that come alive |
|---|---|
| 1 | `domain`, `book` (insert/cancel/FIFO), `config`, `cmd/cexd` health |
| 2 | `book` matching + slow reference matcher (in tests) |
| 3 | `command`, `state` (accounts, ledger, dedupe), `engine` (owner, no disk) |
| 4 | `journal`, replay on startup, `cmd/replay`, crash harness in `tests/` |
| 5 | `api` endpoints, auth, group commit |
| 6 | `projector` + PostgreSQL, `marketdata` WebSocket, minimal UI |
| 7 | metrics, `loadtest/`, `deploy/` compose, profiling into `evidence/` |
| 8 | inspector polish, docs, v0.1 tag |
