# Centralized Exchange (CEX) Simulator in Go

A deterministic spot-exchange simulator with an auditable ledger, a durable
command journal, an authenticated HTTP API, group commit, a replay inspector
and measured crash recovery. It is built as a serious systems project for
learning production Go.

> **Paper trading only.** Simulated funds via explicit funding commands. No
> real custody or customer money.

**v0.1.0 (Tier 1) is released:** [release notes](docs/RELEASE.md) ·
[known limitations](docs/LIMITATIONS.md) · [three-minute demo](docs/DEMO.md) ·
[performance report](docs/PERFORMANCE.md) ·
[crash and restore drills](evidence/crash/2026-10-28/report.md)

```bash
make demo     # the three-minute demonstration, narrated, against the real binary
```

## How it works

```mermaid
flowchart LR
    C[HTTP clients] -->|"POST /v1/orders<br/>Idempotency-Key"| API[internal/api<br/>auth, limits, strict JSON]
    API -->|bounded queue| E[internal/engine<br/>ONE owner goroutine]
    E -->|"1. append + fsync<br/>(group commit)"| J[(journal.wal)]
    E -->|"2. Apply (pure)"| S[internal/state<br/>book, ledger, dedupe]
    E -->|3. reply| API
    E -->|immutable snapshot| MD[internal/marketdata] -->|WebSocket| V[browsers]
    J -->|follows, replays| P[internal/projector] --> PG[(PostgreSQL<br/>read model)]
    J -->|read-only replay| R[cmd/replay<br/>-explain]
    E -.->|metrics| T[internal/telemetry<br/>admin port :6060]
```

One goroutine owns all trading state, so the book and balances need no
locks. A command is journaled and fsynced **before** it is applied and
answered, so an answer means it survives a crash; restart replays the
journal into the same state, byte for byte. Details:
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Status

**Week 8: frozen and demonstrated (v0.1.0).** The replay inspector explains
any order from request to ledger postings (`replay -explain`). Drills kill
the real server mid-traffic five times: every one of 129,063 answered
requests survived, all 233 lost answers were retried with exactly one effect
(84 had run before the crash), money was conserved after every kill, a copied
journal restores to an exact prefix, and recovery takes 8 s for a million
commands. Known limits are written down, with evidence.

**Week 7: measured.** Metrics at three nested levels on a private admin
port with pprof; an open-model load generator; Docker Compose with
Prometheus and Grafana. Group commit serves 3200 orders/s at a 2.4 ms
median where one fsync per order saturates near 750/s; the latency tail is
the disk's rare slow fsyncs, not the code.

| Piece | Package | State |
|---|---|---|
| Units, exact decimal parsing, checked arithmetic | `internal/domain` | ✅ week 1 |
| Order book: price levels, FIFO queues, cancel | `internal/book` | ✅ week 1 |
| Matching: price-time priority, partial fills, IOC, self-trade prevention | `internal/book` | ✅ week 2 |
| Money: reservations, double-entry ledger, settlement, idempotent retries | `internal/state` | ✅ week 3 |
| Engine: single owner goroutine, concurrent submissions, snapshots | `internal/engine` | ✅ week 3 |
| Durable journal (WAL), startup recovery, read-only replay | `internal/journal`, `internal/node`, `cmd/replay` | ✅ week 4 |
| HTTP API, bearer auth, ownership, limits, group commit | `internal/api`, `internal/engine`, `internal/journal` | ✅ week 5 |
| SQL read model: journal-following projector, transactional checkpoint, `/v1/status` lag | `internal/projector` | ✅ week 6 |
| Live WebSocket book feed (slow viewers dropped), minimal web UI | `internal/marketdata`, `internal/webui` | ✅ week 6 |
| Metrics on a private admin port (pprof too), high-resolution clock | `internal/telemetry`, `internal/clock` | ✅ week 7 |
| Open-model load generator, workloads, performance report | `cmd/loadgen`, `loadtest/` | ✅ week 7 |
| Docker image, Compose stack, Prometheus alerts, Grafana dashboards | `deploy/` | ✅ week 7 |
| Replay inspector: `-explain <order>`, `-explain-seq <seq>` | `cmd/replay` | ✅ week 8 |
| Crash, retry, money, restore and recovery-time drills | `tests/drills` | ✅ week 8 |
| Demo program, limitations, release notes, v0.1.0 tag | `tests/demo`, `docs/` | ✅ week 8 |

## Quick start

Requirements: Go 1.26.10+ (go.mod selects it automatically), `make`
(optional, every target is a plain `go` command), Docker Desktop for the
Compose stack and the integration tests. Checked on a clean machine:
[evidence/quickstart](evidence/quickstart/).

```bash
make demo           # the guided tour (about 15 seconds)
make run            # or: go run ./cmd/cexd   (journal in ./data/journal.wal)
# then open http://localhost:8080/   (live book + trade tape; paste a token for the account panel)
curl localhost:8080/healthz   # → ok
curl -i localhost:8080/readyz # → 200 ready (once the journal is recovered)

go run ./cmd/replay -data-dir ./data              # inspect a journal, read-only
go run ./cmd/replay -data-dir ./data -to-seq 6    # the state right after seq 6
go run ./cmd/replay -data-dir ./data -explain 4   # order 4: request, fills in priority order, ledger
go run ./cmd/replay -data-dir ./data -explain-seq 7  # one command: request, decision, postings
```

Checks:

```bash
make check          # fmt-check + vet + race-enabled tests (what CI runs)
go run ./tests/week5 # real HTTP process gate, crash restart and retry safety
make drills         # crash, retry, money, restore, recovery time (about 5 minutes)
make vuln           # govulncheck
```

### The whole stack in Docker

```bash
make up          # exchange :8080, admin/metrics :6060, Prometheus :9090, Grafana :3000
make load-smoke  # a few seconds of load; results in tmp/load-smoke/report.md
make load-step   # 100 -> 3200 orders/s with a CPU profile (about 2.5 minutes)
make down        # stop; volumes are kept
```

The Compose server already knows the load generator's accounts (local demo
credentials, see [deploy/cexd.env](deploy/cexd.env)): paste
`loadgen-local-admin-0001` into the web UI to fund and trade. Profiling a
running server: `go tool pprof http://localhost:6060/debug/pprof/profile?seconds=20`.

### With the SQL read model (needs Docker Desktop running)

```bash
make db-up                    # local PostgreSQL 17 on 127.0.0.1:5433
CEX_DATABASE_URL=postgres://cex:cex-local-only@127.0.0.1:5433/cex make run
curl -H "Authorization: Bearer <token>" localhost:8080/v1/status   # applied_seq, projected_seq, lag
make test-integration         # projector + outage tests against a real PostgreSQL
make db-down
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
one-fsync-per-command baseline. See [ADR-0008](docs/adr/0008-opportunistic-group-commit.md).

## Core guarantees, and what proves them

1. **A success response means the command is durable (fsynced) and applied.**
   Journal before Apply; `tests/crash` (failpoints at every write boundary)
   and the [drills](evidence/crash/2026-10-28/report.md) (129,063 answered
   requests, five hard kills, none lost).
2. **Restarting replays the journal and reproduces the full state exactly.**
   The recovered digest equals an independent offline replay after every
   crash scenario and every drill round; a restored copy matches the
   original's digest at the same Seq.
3. **Retrying a request with the same key never applies it twice.**
   `internal/state` tests, the week-5 gate, and the drills: 233 lost answers
   retried twice each, every one with exactly one effect.
4. **Every asset's ledger sums to zero, and balances never go negative.**
   Checked after every command in randomized tests, at every startup, and
   independently (issued = held) after every drill kill.

The failure model is **process crashes** on a persistent disk. Power loss,
disk loss and machine loss are not claimed; see
[LIMITATIONS.md](docs/LIMITATIONS.md) and
[ADR-0006](docs/adr/0006-journal-format-recovery-and-failure-model.md).

## Documentation

| Read this | For |
|---|---|
| [docs/RELEASE.md](docs/RELEASE.md) | v0.1.0: capabilities, the release gate with evidence, how to verify |
| [docs/LIMITATIONS.md](docs/LIMITATIONS.md) | What is not done, not tested, and where it breaks first |
| [docs/DEMO.md](docs/DEMO.md) | The three-minute demonstration and likely questions |
| [docs/PERFORMANCE.md](docs/PERFORMANCE.md) | Load tests: where the time goes, the bottleneck, the one optimization |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Components, data flow, storage, package layout, API |
| [docs/adr/](docs/adr/) | Architecture decisions and their reasoning |
| [evidence/](evidence/) | Raw benchmark, load, profile, drill and demo results |
| [track/](track/) | Day-by-day notes: what was done, why, and how it works |
| [docs/plan/](docs/plan/) | The master plan |
