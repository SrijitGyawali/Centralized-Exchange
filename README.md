# Centralized Exchange (CEX) Simulator in Go

A deterministic spot-exchange simulator with an auditable ledger, a durable
command journal, a replay inspector and measured crash recovery. It is
built as a serious systems project for learning production Go.

> **Paper trading only.** Simulated funds via explicit funding commands. No
> real custody or customer money.

## Status

**Week 3 complete.** The core and its single-owner engine work and are
tested under the race detector; nothing is durable or reachable over HTTP
yet.

| Piece | Package | State |
|---|---|---|
| Units, exact decimal parsing, checked arithmetic | `internal/domain` | ✅ week 1 |
| Order book: price levels, FIFO queues, cancel | `internal/book` | ✅ week 1 |
| Matching: price-time priority, partial fills, IOC, self-trade prevention | `internal/book` | ✅ week 2 |
| Money: reservations, double-entry ledger, settlement, idempotent retries | `internal/state` | ✅ week 3 |
| Engine: single owner goroutine, concurrent submissions, snapshots | `internal/engine` | ✅ week 3 |
| Durable journal (WAL) and crash recovery | `internal/journal` | ⏳ week 4 |
| HTTP API | `internal/api` | week 5 (health checks only today) |

The server binary starts, serves `/healthz` and `/readyz`, and shuts down
gracefully. See the [build order](docs/ARCHITECTURE.md#8-build-order) and
the day-by-day notes in [track/](track/).

## Quick start

Requirements: Go 1.26.5+, `make` (optional, every target is a plain `go`
command).

```bash
make run            # or: go run ./cmd/cexd
curl localhost:8080/healthz   # → ok
curl -i localhost:8080/readyz # → 503 until the engine exists
make check          # fmt-check + vet + race-enabled tests (what CI runs)
```

Configuration comes from environment variables. See
[.env.example](.env.example).

## Documentation

| Read this | For |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Components, data flow, storage, package layout, API |
| [docs/adr/](docs/adr/) | Architecture decisions and their reasoning |
| [track/](track/) | Day-by-day notes: what was done, why, and how it works |
| [docs/plan/](docs/plan/) | The full 20-week master plan |

## Core guarantees (targets, proven by tests as they are built)

1. A success response means the command is durable (fsynced) **and** applied.
   *(week 4)*
2. Restarting replays the journal and reproduces the full state exactly.
   *(week 4; in-memory replay determinism is already tested)*
3. Retrying a request with the same key never applies it twice.
   ✅ *tested in `internal/state`*
4. Every asset's ledger postings sum to zero, and user balances never go
   negative. ✅ *checked after every command in randomized tests*
