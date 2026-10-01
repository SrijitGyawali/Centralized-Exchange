# Centralized Exchange (CEX) Simulator in Go

A deterministic spot-exchange simulator with an auditable ledger, a durable
command journal, a replay inspector and measured crash recovery. It is
built as a serious systems project for learning production Go.

> **Paper trading only.** Simulated funds via explicit funding commands. No
> real custody or customer money.

## Status

**Day 1: project skeleton.** The server binary starts, serves `/healthz`
and `/readyz`, and shuts down gracefully. Domain code starts in week 1,
session 2. See the [build order](docs/ARCHITECTURE.md#8-build-order).

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
2. Restarting replays the journal and reproduces the full state exactly.
3. Retrying a request with the same key never applies it twice.
4. Every asset's ledger postings sum to zero, and user balances never go
   negative.
