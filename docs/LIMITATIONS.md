# Known limitations (v0.1)

What this exchange does **not** do, what has **not** been tested, and where
it would break first. Every limit names its evidence and what would lift
it. The capability list is in [RELEASE.md](RELEASE.md); the measurements
in [PERFORMANCE.md](PERFORMANCE.md).

It is a **paper-trading simulator**: test money is created by an admin
command, nothing is ever withdrawn, and it must never hold real funds.

---

## 1. Failure model: what has been tested

| Failure | Status | Evidence |
|---|---|---|
| Process crash (hard kill: SIGKILL / TerminateProcess) under load | ✅ Tested: no acknowledged command lost, every lost response retried with exactly one effect, money conserved | [drills](../evidence/crash/2026-10-28/report.md), `tests/crash`, `go run ./tests/e2e` |
| Crash at every journal boundary (before / mid / after write, after fsync) | ✅ Tested with failpoints, deterministically | `tests/crash` |
| Torn final frame | ✅ Cut off at startup (it was never acknowledged) | `internal/journal`, `tests/crash` |
| Corrupt complete frame | ✅ Startup **refuses** and leaves the file untouched | `internal/journal` tests, ADR-0006 |
| Restore from a copied journal (hot or cold copy) | ✅ Exact prefix, same digest at the same Seq, loses only what came after the copy | [drills](../evidence/crash/2026-10-28/report.md) |
| PostgreSQL frozen under load | ✅ Trading unaffected; read model lags, then catches up | [outage drill](../evidence/load/2026-10-27-outage-under-load/report.md) |
| **Power loss / kernel crash** | ❌ **Not tested.** Durability rests on `fsync` (Windows `FlushFileBuffers`) and on the drive honouring flushes. A process kill does not exercise that: the OS survives with its page cache | — |
| **Machine or disk loss** | ❌ **Data loss up to the last copy.** One journal on one disk; no replication | — |
| Docker Desktop volumes | ⚠️ Process kills only. The volume is a virtual disk in a VM: fsync there is slower (p50 3-4 ms) and its guarantees are the VM's | [outage drill](../evidence/load/2026-10-27-outage-under-load/report.md) |

**What would lift it:** power-cut tests on real hardware (or a VM whose
disk cache can be dropped), then replication (a follower applying the same
journal; the state machine is already deterministic, which is the hard part).

## 2. Scope (Tier 1)

- **One symbol**, BTC-USDT. No cross-symbol risk, no symbol configuration.
- **Limit orders only**: GTC and IOC, plus cancel. No market, stop, FOK,
  post-only or amend orders.
- **No fees.** (The plan's Tier 2 has a worked two-fee example.)
- **Self-trade prevention has one policy**: stop matching and cancel the
  taker's remainder.
- **Funding is an admin command**; no deposits, withdrawals or transfers.
- **No order history API**: the SQL read model holds history, but no
  endpoint serves it yet.

## 3. Capacity and memory

| Limit | Measured | Why | What would lift it |
|---|---|---|---|
| Throughput is bounded by **one** writer and its fsync | Group commit: 3200 orders/s at a 2.4 ms median on a laptop NVMe; one fsync per order: ~750/s | ADR-0001, ADR-0008 | Faster storage first; more symbols under the same writer; sharding is a different consistency problem |
| **Latency tail**: rare 100 ms - 1 s fsyncs stall everything queued | 4-9 per run over 100 ms | The drive and OS, not the code ([lab](../labs/fsync-tail/NOTES.md)) | Drives with power-loss protection |
| **Memory grows forever**: every idempotency key and result is kept | ~370 B live per command (heap profile); 432 MiB of heap after recovering 1M commands | ADR-0004, Tier 1 dedupe retention | Expiry with a documented retry window, done by replayed expiry commands |
| **Recovery replays the whole journal** | 1.2 s at 100k commands, 8.0 s at 1M ([recovery drill](../evidence/crash/2026-10-28/report.md)); a million commands is under 3 hours of trading at 100 orders/s | No snapshots in Tier 1 | Snapshots + journal segments (Tier 2) |
| **The journal is one growing file** | ~45 bytes per command (42.5 MiB per million) | Tier 1 design | Segments, archiving, retention |
| **The book is unbounded** | — | No per-user open-order limit | A per-user limit checked in `Apply` |
| Intake queue of 1024 absorbs only ~0.3 s of stalls at 3200/s | refusals at 3200/s | A fail-fast policy, configurable (`CEX_QUEUE_SIZE`) | A deliberate choice per deployment |

## 4. Security and operations

- **Authentication is static bearer tokens** from an environment variable:
  no rotation or revocation without a restart, no login, no expiry. Tokens
  are matched by map lookup (not a constant-time comparison) and the scheme
  word `Bearer` is case-sensitive.
- **No TLS.** Put a TLS-terminating proxy in front for anything not local.
- **Rate limits are per user, in memory**, per process.
- **The admin port** (`/metrics`, `/debug/pprof`) is on loopback by default;
  anyone who can reach it can profile the process. Keep it private.
- **The market-data stream is public**, full snapshots only; no
  incremental updates and no private order updates (Tier 2).
- **Demo credentials are published** in `deploy/cexd.env` and the test
  programs. Local use only.
- **The projector cannot tell a hung database from a slow one**: a frozen
  PostgreSQL raises no error, so `healthy` stays 1 and only the lag grows.
  Fix: a statement timeout. The lag alert fires meanwhile.
- **Corruption needs a human.** Startup refuses a corrupt journal and there
  is no repair tool; `cmd/replay` can inspect it read-only.
- **Alert thresholds are first guesses**, and there is no Alertmanager:
  alerts only show on Prometheus's page.

## 5. Software bugs

`Apply` is deterministic, so a logic bug is replayed faithfully: replay
reproduces it instead of fixing it. Defences: invariants are checked at
every startup (a violation refuses to start), a panic or apply error halts
the engine (readiness turns 503, mutations stop), and the reference tests
compare the book against a simple model. A bug that keeps every invariant
true but computes the wrong price would still be journaled.

## 6. Measurement caveats

- All numbers come from **one Windows laptop**, with the load generator on
  the same machine, and **one run per configuration**; fsync stalls make
  single-run p99s noisy.
- On Windows, Go's `time.Now` ticks every ~0.5 ms (measurements use
  `internal/clock`), and the CPU profiler counts threads blocked in system
  calls as running.
- The race detector only sees the interleavings the tests exercise.

## 7. Repository history

- Commit `68facb6` does not compile on its own (`stats.go` was committed
  before `scrape.go`, which defines `ServerStats`); every commit from
  `037c8a6` on builds. Use `git bisect skip` if it is ever hit.
- Ten week-7 commits contain two timing tests that are flaky on Windows;
  fixed in `abcdc50`.
