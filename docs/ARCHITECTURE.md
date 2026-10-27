# Architecture and project structure

This document describes how the backend is organised: the main components,
how a request travels through them, where data is stored, and which
directory or package owns each responsibility. It explains the *why* behind
the repository layout. Detailed decisions live in [ADRs](adr/), and the full
roadmap is in the [master plan](plan/CEX_Go_Guided_Master_Plan.md).

> Status: weeks 1-7 implemented (week 7: Prometheus metrics, private admin port with pprof, load generator, Docker Compose stack, Grafana dashboards). Each section notes the week it was built.

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
 (REST, JSON)    validate, auth,   (buffered chan,    1. assign sequence                   │
                 parse decimals    reject when full)  2. internal/journal: append + Sync ──┼──► data/journal.wal
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

 cmd/replay (offline CLI) ── reads data/journal.wal (read-only) ── state.Apply ── prints state / explains an order
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
| Journal (WAL) | `internal/journal` | Canonical command codec; frames with magic, version, length, Seq and separate header/payload CRC-32C; append + `Sync`; recovery scan (torn tail truncated, corruption refused). See ADR-0006 | 4 |
| Node | `internal/node` | Startup recovery (open journal, replay into a fresh state, check invariants, digest) and engine lifecycle. Used by `cexd`, the crash harness and tests | 4 |
| Engine | `internal/engine` | Owner goroutine: intake queue, sequencing, group commit, calls `Apply`, delivers replies, publishes read snapshots, halts on storage errors | 3-5 |
| HTTP API | `internal/api` | Routing, JSON validation, auth, ownership checks, rate limits, status-code mapping, health/readiness | 1 (health), 5 |
| Market data | `internal/marketdata` | Hub polls the lock-free engine snapshot (bounded rate), encodes full snapshots once, one bounded buffer per WebSocket viewer, slow viewers disconnected with a reason. See ADR-0010 | 6 |
| Web UI | `internal/webui` | Embedded HTML/CSS/JS: live book, trade tape, seq and projection lag, balances, order form; strict CSP, textContent only | 6 |
| Projector | `internal/projector` | Follows `journal.wal` read-only, replays it through a private `state.State`, writes commands/orders/trades/ledger/balances plus the checkpoint in ONE transaction (checkpoint row locked first), reconciles at the same Seq. See ADR-0009 | 6 |
| Config | `internal/config` | Reads environment variables into a typed, validated `Config` | 1 |
| Server binary | `cmd/cexd` | Composition root: builds every component, wires them, runs, shuts down gracefully | 1 |
| Replay CLI | `cmd/replay` | Offline replay to a chosen sequence, state digest, "explain order" | 4, 8 |
| Telemetry | `internal/telemetry` | The ONLY importer of the Prometheus client. Turns the engine Observer, journal OnSync, hub Stats, projector Status and API route names into metrics; HTTP middleware; served on the private admin port. See ADR-0011 | 7 |
| Clock | `internal/clock` | High-resolution monotonic clock for measuring (QueryPerformanceCounter on Windows, where `time.Now` ticks every ~0.5 ms). Never used by the state machine | 7 |
| Load generator | `cmd/loadgen` | Open-model workloads from versioned JSON, exact client percentiles, per-stage server attribution from `/metrics`, raw results. See ADR-0012 | 7 |

SQL migrations live inside `internal/projector/migrations` and the web client
inside `internal/webui/static`, both compiled into the binary with `go:embed`.

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
| Commands (every sequenced command, including rejections and duplicates) | `data/journal.wal` (one append-only file; segments arrive with snapshots in Tier 2) | **Yes** | It *is* the durable record |
| Books, balances, reservations, dedupe results, ID counters | Memory, inside `state.State` | Derived | Replaying the WAL (Tier 2: snapshot + WAL suffix) |
| Snapshots (Tier 2) | `data/snapshots/` | Derived, a recovery shortcut | Checksummed and atomically published |
| Commands, orders (full lifecycle), trades, ledger journals and lines, balances | PostgreSQL tables (optional: `CEX_DATABASE_URL`) | **No**, a disposable projection | Rebuilt from the journal; `projection_checkpoint` commits with the rows; lag shown by `GET /v1/status` |
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
│   ├── loadgen/              #   Open-model load generator (week 7)
│   └── replay/               #   Offline replay / inspector CLI
├── internal/                 # Private code. The Go toolchain forbids imports from other modules.
│   ├── domain/               #   Pure: units, checked math, enums, IDs
│   ├── command/              #   Pure: command and result data types
│   ├── book/                 #   Pure: order book + matching
│   ├── state/                #   Pure: full state + Apply
│   ├── journal/              #   I/O:  WAL files
│   ├── engine/               #   Concurrency: the single owner goroutine
│   ├── node/                 #   Wiring: startup recovery + engine lifecycle
│   ├── api/                  #   I/O:  HTTP handlers
│   ├── marketdata/           #   I/O:  WebSocket feeds
│   ├── webui/                #   The embedded web client (static files)
│   ├── projector/            #   I/O:  PostgreSQL read model
│   ├── telemetry/            #   Prometheus metrics (the only importer of the client)
│   ├── clock/                #   High-resolution clock for measuring durations
│   └── config/               #   Startup configuration
├── tests/                    # Cross-package tests: crash harness, end-to-end, recovery
├── loadtest/                 # Versioned workloads (JSON) and the outage drill (week 7)
├── deploy/                   # Dockerfile, docker-compose, Prometheus, Grafana (week 7)
├── labs/                     # Small Go runtime experiments (scheduler, GC, escape analysis…)
├── evidence/                 # Benchmark output, profiles, crash reports, demo recordings
├── track/                    # Day-by-day notes (track/2026-10-01/…): what, why, how
├── docs/
│   ├── ARCHITECTURE.md       #   This file
│   ├── adr/                  #   Architecture Decision Records, one decision per file
│   └── plan/                 #   The master plan
├── .github/workflows/        # CI: fmt, vet, race tests, integration, govulncheck, image + promtool
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
- `internal/telemetry` sits at the edge with `cmd/cexd`: it imports engine,
  node, marketdata and the Prometheus client; nothing imports it except
  `cmd/*`. Producers report through hooks they declare themselves.
- Cycles are compile errors in Go, so a bad dependency shows up immediately.

---

## 7. HTTP API (implemented, week 5)

| Method & path | Purpose | Notes |
|---|---|---|
| `GET /healthz` | Liveness: the process is running | Day 1 |
| `GET /readyz` | Readiness: replay done, trading engine not halted | 503 after a storage failure |
| `GET /v1/session` | Verify a provisioned bearer token | Returns user ID and admin role |
| `POST /v1/orders` | Place an order | Body: `symbol`, `side`, `type`, `time_in_force`, `price`, `quantity` (strings). `Idempotency-Key` header; returns `order_id`, `outcome`, `seq` |
| `DELETE /v1/orders/{id}` | Cancel (sequenced command) | Requires a request key, owner only |
| `GET /v1/orders/{id}` | Resting order status | Owner only; filled/canceled/foreign/unknown IDs return 404 |
| `GET /v1/requests/{client_key}` | Result of an earlier request | Lost-response recovery |
| `GET /v1/balances` | Available and reserved per asset | Tagged `seq` |
| `GET /v1/book?symbol=BTC-USDT` | Depth snapshot | Tagged `seq` |
| `POST /v1/admin/fund` | Create simulated money | Admin only; `user_id`, `asset`, `amount` strings |
| `GET /v1/status` | Engine and read-model progress | `applied_seq`, and `projection.projected_seq` / `lag` / `healthy` (week 6) |
| `GET /stream/book` (WebSocket) | Public full snapshots: depth + last 50 trades, tagged with seq | No token; slow viewers get close 1013. Week 6 |
| `GET /` | The web client | Static, embedded |

All `/v1` routes require bearer authentication. Auth precedes per-user rate
limiting and strict, bounded JSON decoding. Status codes: `200` accepted/read,
`400` invalid input, `401`/`403` auth, `404` unknown/private order or request,
`409` key conflict, `413` oversized body, `415` content type, `422` durable
business rejection, `429` rate limited, `503` unavailable, `504` deadline.
Admitted failures explicitly report `outcome_unknown`; clients reuse their key.
Money and large IDs are JSON **strings**. See [ADR-0007](adr/0007-authenticated-http-api.md).

The engine groups immediately available queued commands, up to BatchSize,
without a timer. Queries split batches and retain queue order. Production
AppendBatch validates all frames, writes once and syncs once before Apply or
reply. Failures halt the engine; recovery can replay a complete batch prefix.
The WAL frame format is unchanged. See [ADR-0008](adr/0008-opportunistic-group-commit.md).

### Operator endpoints (private admin port, week 7)

Served on `CEX_ADMIN_ADDR` (default `127.0.0.1:6060`, `off` disables), never
on the public port. See [ADR-0011](adr/0011-metrics-and-private-admin-port.md).

| Method & path | Purpose |
|---|---|
| `GET /metrics` | Prometheus text format: HTTP, engine (queue wait, durable, apply, total, outcomes, refusals, queue length), journal (write, fsync, bytes, size), recovery, projection, market data, Go runtime, process |
| `GET /debug/pprof/` | Profiles: `profile` (CPU), `heap`, `allocs`, `goroutine`, `block`, `mutex`, `trace` |

`cexd healthcheck` (a subcommand, not an endpoint) asks `/readyz` from inside
the container and exits 0 or 1.

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
| 7 | ✅ `telemetry`, `clock`, `cmd/loadgen`, `loadtest/`, `deploy/` (Compose, Prometheus, Grafana), profiles and load results in `evidence/` |
| 8 | inspector polish, docs, v0.1 tag |
