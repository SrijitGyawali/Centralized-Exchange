# v0.1.0: Tier 1

The first release: a complete, measured, single-symbol paper-trading
exchange. Git tag `v0.1.0` (Go modules require full semantic versions, so
"v0.1" is tagged as `v0.1.0`).

**Not for real money.** Read [LIMITATIONS.md](LIMITATIONS.md) first.

## Quick start

```bash
git clone https://github.com/SrijitGyawali/Centralized-Exchange.git && cd Centralized-Exchange
make demo      # the three-minute demonstration, narrated (Go 1.26.10+ only)
make up        # exchange + PostgreSQL + Prometheus + Grafana in Docker
```

The quick start was checked on a clean machine (a fresh `golang:1.26.10`
container, cloning from GitHub): see
[`evidence/quickstart/`](../evidence/quickstart/).

## Tier 1 capabilities

**Trading**
- One symbol, BTC-USDT, with exact fixed-point units: prices in whole USDT
  ticks, quantities in 0.0001 BTC lots, money in 0.000001 USDT atoms; no
  floating point anywhere ([ADR-0002](adr/0002-units-and-fixed-point.md)).
- GTC and IOC limit orders and cancels; price-time priority; fills at the
  maker's price; self-trade prevention (cancel the taker's remainder).

**Money**
- Reservations: every open order holds its funds, so nothing can be spent
  twice; a double-entry ledger where every journal sums to zero per asset;
  a supply cap; invariants checked at every startup
  ([ADR-0004](adr/0004-accounting-reservations-ledger-idempotency.md)).
- Idempotency: every mutation carries a key; a retry returns the stored
  result, and a different request under the same key is refused (409).

**Durability**
- A write-ahead journal: frames with separate header and payload
  CRC-32C, fsynced before any command is applied or answered; group commit
  shares one fsync among queued commands with no batching timer.
- Recovery by replay: a torn final frame is cut off, a corrupt frame
  refuses startup, the recovered state's digest is logged
  ([ADR-0006](adr/0006-journal-format-recovery-and-failure-model.md),
  [ADR-0008](adr/0008-opportunistic-group-commit.md)).

**Interfaces**
- An authenticated HTTP API with ownership checks, strict JSON, per-user
  rate limits, deadlines, and an explicit `outcome_unknown` answer
  ([ADR-0007](adr/0007-authenticated-http-api.md)).
- A public WebSocket book and trade feed that drops slow viewers instead
  of slowing matching, and a minimal web UI ([ADR-0010](adr/0010-public-market-data-feed-and-web-ui.md)).
- A PostgreSQL read model that follows the journal with transactional
  checkpoints and reports its lag ([ADR-0009](adr/0009-sql-read-model-projection.md)).
- `cmd/replay`: read-only replay to any Seq, the state digest, and
  `-explain` / `-explain-seq` to trace an order from request to ledger
  postings.

**Operations**
- Prometheus metrics on a private admin port with pprof; four Grafana
  dashboards; alert rules; a distroless, non-root container image; Docker
  Compose for the whole stack ([ADR-0011](adr/0011-metrics-and-private-admin-port.md)).
- `cmd/loadgen`: open-model load tests with server-side attribution
  ([ADR-0012](adr/0012-open-model-load-testing.md)).
- `tests/drills`: crash, retry, money, restore and recovery-time drills.

## Release gate (from the master plan)

| Gate item | Status | Evidence |
|---|---|---|
| One symbol with documented GTC/IOC/cancel/self-trade semantics and exact fixed-point units | ✅ | `internal/book` (rules in `book.Place`'s comment), ADR-0002, `replay -explain` |
| Reference tests and money/reservation invariants pass for recorded and generated cases | ✅ | `internal/book/random_test.go` (reference model), `internal/state` invariant and random tests, golden session fixtures, fuzzing |
| WAL errors halt mutation; partial-tail and complete-frame corruption are handled differently | ✅ | `internal/engine` halt tests, `internal/journal` torn-tail vs corruption tests, ADR-0006 |
| Full-state replay agrees; acknowledged operations survive process crash; retries cannot duplicate effects | ✅ | `tests/crash`, `tests/week5`, [drills](../evidence/crash/2026-10-28/report.md) (digest equality, every answer kept, exactly-once retries) |
| API ownership, queue limits, timeout behavior and query consistency are documented and tested | ✅ | ADR-0005, ADR-0007, `internal/api` tests, `internal/engine` overload and timeout tests |
| Live view and ledger are inspectable; SQL rebuild/checkpoint tests pass | ✅ | web UI and `/stream/book`, `replay -explain`, `internal/projector` integration tests |
| Load report separates pure matching from durable and HTTP latency, including rejection/error rates | ✅ | [PERFORMANCE.md](PERFORMANCE.md), [`evidence/load/`](../evidence/load/) |
| Fresh-machine quickstart, replay demo, limitations, and release tag exist | ✅ | [quickstart check](../evidence/quickstart/), [DEMO.md](DEMO.md) + [recording](../evidence/demo/), [LIMITATIONS.md](LIMITATIONS.md), tag `v0.1.0` |

The plan's gate also names "a demo recording": the recording here is the
demo program's own transcript (text), not a video. A screen recording can
be made from `make demo` with the spoken script in [DEMO.md](DEMO.md).

## Measured (one laptop; see PERFORMANCE.md for the method)

| | |
|---|---|
| Group commit | 3200 orders/s offered → 3193 answered/s, p50 2.4 ms |
| One fsync per order | saturates at ~750/s; 58-76 % refused beyond |
| Engine time at 400/s (p50) | apply 0.04 ms, durable 1.5 ms (fsync 1.45), engine 1.6 ms, HTTP 1.7 ms |
| 100 orders/s for 10 minutes | p99 45.3 ms, 0 errors of 60,000 |
| Crash under load (5 hard kills, 32 clients) | 129,063 of 129,063 answered requests kept; 233 of 233 lost answers retried with exactly one effect (84 had run before the crash); money conserved after every kill |
| Recovery time (no snapshots) | 100k commands: 1.2 s; 1M commands: 8.0 s (42.5 MiB journal, ~125k commands/s, 432 MiB heap) |

## Verifying this release

```bash
make check                 # gofmt, vet, race-enabled tests
make test-integration      # PostgreSQL (Docker)
go run ./tests/week5       # HTTP crash and retry gate
make drills                # crash, retry, money, restore, recovery time
make demo
make vuln                  # govulncheck: 0 reachable vulnerabilities at release
```

CI runs all of these except the full drills (a quick drill runs instead)
on every push.
