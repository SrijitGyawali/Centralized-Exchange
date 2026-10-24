# Centralized Exchange (CEX) Simulator in Go

A deterministic spot-exchange simulator with an auditable ledger, a durable
command journal, an authenticated HTTP API, group commit, a replay inspector
and measured crash recovery. It is
built as a serious systems project for learning production Go.

> **Paper trading only.** Simulated funds via explicit funding commands. No
> real custody or customer money.

## Status

**Week 5 implemented.** HTTP clients can authenticate with provisioned bearer
tokens, place/cancel orders, query private results and balances, read the book,
and fund simulated accounts with an admin token. Queued commands share a journal
write and fsync before any apply or reply. A lone request has no batching delay.
Restart replays the same WAL format, and retries keep their original outcome.

| Piece | Package | State |
|---|---|---|
| Units, exact decimal parsing, checked arithmetic | `internal/domain` | ✅ week 1 |
| Order book: price levels, FIFO queues, cancel | `internal/book` | ✅ week 1 |
| Matching: price-time priority, partial fills, IOC, self-trade prevention | `internal/book` | ✅ week 2 |
| Money: reservations, double-entry ledger, settlement, idempotent retries | `internal/state` | ✅ week 3 |
| Engine: single owner goroutine, concurrent submissions, snapshots | `internal/engine` | ✅ week 3 |
| Durable journal (WAL), startup recovery, replay inspector | `internal/journal`, `internal/node`, `cmd/replay` | ✅ week 4 |
| HTTP API, bearer auth, ownership, limits, group commit | `internal/api`, `internal/engine`, `internal/journal` | ✅ week 5 |

The server recovers its state from `CEX_DATA_DIR` on start, reports ready
once recovery succeeds, and shuts down gracefully (stop HTTP, drain the
engine, close the journal). See the
[build order](docs/ARCHITECTURE.md#8-build-order) and the day-by-day notes
in [track/](track/).

## Quick start

Requirements: Go 1.26.5+, `make` (optional, every target is a plain `go`
command).

```bash
make run            # or: go run ./cmd/cexd   (journal in ./data/journal.wal)
curl localhost:8080/healthz   # → ok
curl -i localhost:8080/readyz # → 200 ready (once the journal is recovered)
make check          # fmt-check + vet + race-enabled tests (what CI runs)
go run ./tests/week5 # real HTTP process gate, crash restart and retry safety

go run ./cmd/replay -data-dir ./data            # inspect a journal, read-only
go run ./cmd/replay -data-dir ./data -to-seq 6  # the state right after seq 6
```

Configuration comes from environment variables. See
[.env.example](.env.example). Protected API routes deny access until
`CEX_API_TOKENS` contains credentials. Health checks need no token.
See [ADR-0007](docs/adr/0007-authenticated-http-api.md) for the full API contract,
status codes and runnable funding/order examples. `GET /v1/session` verifies
the bearer token; there is no password or token-issuance service.

`GET /v1/orders/{id}` reports resting orders owned by the caller. Filled or
canceled orders return 404; use `GET /v1/requests/{key}` for the original
request outcome. A timed-out admitted request may still execute: retry the
same `Idempotency-Key`. Never replace its key just because the response was lost.

Group commit defaults to 64 queued commands. Set `CEX_BATCH_SIZE=1` for the
one-fsync-per-command baseline. See [ADR-0008](docs/adr/0008-opportunistic-group-commit.md)
and [benchmark evidence](evidence/bench/2026-10-23-group-commit.txt).

## Documentation

| Read this | For |
|---|---|
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Components, data flow, storage, package layout, API |
| [docs/adr/](docs/adr/) | Architecture decisions and their reasoning |
| [track/](track/) | Day-by-day notes: what was done, why, and how it works |
| [docs/plan/](docs/plan/) | The full 20-week master plan |

## Core guarantees (targets, proven by tests as they are built)

1. A success response means the command is durable (fsynced) **and** applied.
   ✅ *journal before Apply; acknowledged commands survive process crashes in
   `tests/crash`*
2. Restarting replays the journal and reproduces the full state exactly.
   ✅ *recovered digest equals an independent replay, after every crash
   scenario*

The failure model is **process crashes** on a persistent disk. Power loss,
disk loss and machine loss are not yet claimed; see
[ADR-0006](docs/adr/0006-journal-format-recovery-and-failure-model.md).
3. Retrying a request with the same key never applies it twice.
   ✅ *tested in `internal/state`*
4. Every asset's ledger postings sum to zero, and user balances never go
   negative. ✅ *checked after every command in randomized tests*
