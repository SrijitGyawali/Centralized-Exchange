# Performance report (week 7)

What this exchange does under load, where the time goes, and where it
breaks, measured on one machine. Every number links to the raw results it
came from. Nothing here is a service-level promise.

> **Week-7 gate:** the report distinguishes engine time from durable latency
> and HTTP latency, includes errors and saturation, and links raw results.

## Environment

| | |
|---|---|
| Machine | Windows 11 laptop, Intel i7-1165G7 (4 cores / 8 threads), 7.7 GiB RAM, on AC power |
| Storage | KIOXIA 512 GB NVMe SSD, NTFS; the journal on the host's disk (not Docker) |
| Go | go1.26.10 |
| Server | `cexd` at the commit named in each run's `results.json`, fresh data directory per run, rate limits raised out of the way |
| Load generator | `cmd/loadgen` on the SAME machine (it competes for CPU near saturation) |
| Workloads | [`loadtest/workloads/`](../loadtest/workloads/): 50 users, mix rest / cross / cancel = 45 / 35 / 20, generous funding |

How to read a run: [`loadtest/README.md`](../loadtest/README.md). Method
decisions: [ADR-0012](adr/0012-open-model-load-testing.md). What each metric
measures: [ADR-0011](adr/0011-metrics-and-private-admin-port.md).

## The three latencies

```
client latency   (loadgen: from the SCHEDULED arrival to the response; exact percentiles)
 └─ HTTP          cex_http_request_duration_seconds   handler start → return (answered requests)
     └─ engine    cex_engine_command_seconds          admission → reply
          ├─ queue wait   cex_engine_queue_wait_seconds   waiting for the owner goroutine
          ├─ durable      cex_engine_durable_seconds      journal write + fsync, ONE per batch
          └─ apply        cex_engine_apply_seconds        the pure transition + snapshot + reply
```

The gap between two levels is a finding in itself: client minus HTTP is
the network stack and the generator; HTTP minus engine is JSON, auth and
scheduling; engine minus apply is waiting (for the queue or the disk).

## Before measuring: two findings about the ruler itself

**1. Windows' `time.Now` ticks every 578 µs.** The first runs reported the
engine's apply time as "0.001 ms at p50, 0.75 ms at p99": impossible for a
few microseconds of work. A 20-line experiment showed that 199,999 of
200,000 back-to-back `time.Now()` readings were equal and the smallest step
was 578 µs: Go's monotonic clock on Windows reads the kernel's interrupt
time. Every duration below ~0.6 ms was invisible, and fsync times were
rounded to multiples of 0.58 ms. Fix: [`internal/clock`](../internal/clock)
reads `QueryPerformanceCounter` (100 ns) on Windows. Cost: ~90 ns per
reading against ~6 ns for `time.Now`
([bench](../evidence/bench/2026-10-27-clock-and-observer.txt)). Linux,
including Docker, was never affected.

**2. A mislabelled run.** A "batch = 1" run showed batches of 5 and
1,600 orders/s, impossible with one ~1.2 ms fsync per order. The server
had refused to start ("address in use"): an orphaned batch-64 server from an
earlier test still held the port and answered instead. The report caught it
because the batch limit is read from the server's metrics, not typed by
hand. The experiment script now refuses busy ports and checks the recorded
limit. (A third, smaller one: the first runs were stamped `-dirty` because
two Markdown files were edited while they ran. They were thrown away and
re-run from a clean worktree; every run below names a clean commit.)

## The runs

| Run | Commit | Raw results |
|---|---|---|
| Step 100 → 3200/s, group commit (batch 64) | `b3c2d12` | [`2026-10-27-step-batch64`](../evidence/load/2026-10-27-step-batch64/report.md) (+ `cpu.pprof`) |
| Step 100 → 3200/s, fsync per order (batch 1) | `b3c2d12` | [`2026-10-27-step-batch1`](../evidence/load/2026-10-27-step-batch1/report.md) |
| Steady 100/s for 10 minutes (batch 64) | `b3c2d12` | [`2026-10-27-steady-100`](../evidence/load/2026-10-27-steady-100/report.md) |
| Step, batch 64, after the one optimization | `a384d9a` | [`2026-10-27-step-batch64-after-observer`](../evidence/load/2026-10-27-step-batch64-after-observer/report.md) (+ `cpu.pprof`) |
| Step, batch 64, queue 8192 instead of 1024 | `a384d9a` | [`2026-10-27-step-batch64-queue8192`](../evidence/load/2026-10-27-step-batch64-queue8192/report.md) |
| Outage drill: PostgreSQL frozen 30 s + 5 slow viewers, in Docker | `a384d9a` | [`2026-10-27-outage-under-load`](../evidence/load/2026-10-27-outage-under-load/report.md) |

Each directory holds `requests.csv.gz` (one row per arrival),
`timeline.csv` (server state every second), `metrics-end.prom` and
`results.json`; the tables below are copied from their `report.md`.

## Result 1: group commit vs one fsync per order

Client latency from the scheduled arrival (ms), and the share of requests
that failed (503: refused because the intake queue was full):

| offered / s | batch 64: answered / s | p50 | p99 | errors | batch 1: answered / s | p50 | p99 | errors |
|---|---|---|---|---|---|---|---|---|
| 100 | 100 | 2.83 | 145.17 | 0 % | 100 | 3.03 | 4.79 | 0 % |
| 200 | 200 | 2.67 | 3.83 | 0 % | 200 | 2.52 | 3.69 | 0 % |
| 400 | 400 | 2.82 | 7.67 | 0 % | 400 | 1.98 | 20.75 | 0 % |
| 800 | 772 | 2.54 | 1905.11 | 3.5 % | 800 | 128.76 | 3828.43 | 0 % |
| 1600 | 1600 | 2.72 | 13.58 | 0 % | **675** | **1181.22** | 3978.58 | **57.8 %** |
| 3200 | 3193 | 2.42 | 316.25 | 0.2 % | **758** | **1258.51** | 2917.36 | **76.3 %** |

- **With one fsync per order the engine cannot exceed ~1 / fsync ≈ 700-760
  commands/s.** Past that the queue fills, the median becomes over a second
  (every request waits for ~1,000 others' fsyncs), and most requests are
  refused. Exactly what the 1.2 ms fsync predicts.
- **With group commit, 3200/s is served at a 2.4 ms median.** The batch
  grows with load on its own (mean 1 → 1.4 → 2.1 → 5.3 commands per fsync
  from 400 to 3200/s), because the engine takes whatever is already queued;
  there is no timer, so a lone request never waits.
- **The p99 column is not monotonic in load, in either mode** (batch 64:
  145 ms at 100/s, 3.8 ms at 200/s, 1905 ms at 800/s, 13.6 ms at 1600/s).
  That is the fsync tail (result 3): a stall of 100 ms - 1 s lands in
  whichever stage it happens to hit. One run per configuration cannot rank
  differences smaller than that noise.

## Result 2: where the time goes (the gate)

Server view of the batch-64 step run (histogram estimates, ms, p50 / p99):

| offered / s | HTTP (answered) | engine total | queue wait | durable (per batch) | batch | apply | fsync |
|---|---|---|---|---|---|---|---|
| 100 | 1.743 / 89.286 | 1.607 / 78.000 | 0.027 / 37.500 | 1.494 / 14.617 | 1 | 0.038 / 0.393 | 1.433 / 13.783 |
| 400 | 1.688 / 6.761 | 1.606 / 6.646 | 0.026 / 2.538 | 1.530 / 2.684 | 1 | 0.037 / 0.100 | 1.446 / 2.395 |
| 1600 | 2.112 / 10.347 | 1.995 / 9.935 | 0.691 / 5.674 | 1.206 / 2.285 | 2.1 | 0.011 / 0.082 | 1.184 / 2.171 |
| 3200 | 2.441 / 304.092 | 2.318 / 303.848 | 0.834 / 264.169 | 1.326 / 5.700 | 5.3 | 0.009 / 0.090 | 1.273 / 5.319 |

Reading the median row at 400/s from the inside out:

- **apply 0.04 ms**: the matching engine, ledger, snapshot publish and
  reply. It falls to ~0.01 ms at high load (warm caches, and the owner
  goroutine stays awake between commands).
- **durable 1.5 ms**: one journal write + fsync; fsync is 1.45 of it. The
  write itself is microseconds (`cex_journal_write_seconds`).
- **queue wait 0.03 ms** at low load, 0.7-0.8 ms at high load: a command
  arriving during an fsync waits for it to finish, then shares the next one.
- **engine total 1.6 ms = queue + durable + apply.** Durability is ~95 % of
  the engine's time.
- **HTTP 1.7 ms**: the engine plus ~0.1 ms of JSON decoding, auth, rate
  limiting and routing.
- **client 2.8 ms**: HTTP plus ~1 ms of Windows loopback networking, the
  generator's own scheduling, and the client-side HTTP stack (the
  generator shares the machine).

**Errors and saturation** (same run): refusals only at 800/s (555) and
3200/s (138), both caused by stalls that outlasted the queue: 1024 slots
absorb 1.3 s of arrivals at 800/s but only 0.32 s at 3200/s. Process CPU
peaked at 1.06 cores of 8; goroutines at 2,205 (one per open connection);
RSS at 172 MiB.

## Result 3: the bottleneck is the fsync tail, not the CPU

The CPU profile taken at 3200/s
([analysis](../evidence/profiles/2026-10-27-step-3200-fsync-bound.md)):
the owner goroutine spends about **93 % of its time inside
`FlushFileBuffers`** (Windows' fsync). Its CPU work, apply plus publish
plus metrics, is under 1 s of 18. On Windows the profiler counts threads
blocked in a system call, so this is waiting, not computing: the process
used ~1 core of 8.

The fsync distribution has a long, rare tail. Per run on the host, between
1 in 90 and 1 in 310 fsyncs took over 10 ms, 4-9 took over 100 ms and up
to 5 over 250 ms (none over 1 s; in Docker's VM, 16 over 250 ms and one
over 1 s in two minutes). The single writer cannot do anything else
meanwhile, so one stall delays every queued request: that produced the
1.9 s p99 at 800/s.

Two hypotheses were tested in a separate lab
([`labs/fsync-tail`](../labs/fsync-tail/NOTES.md)):

1. *File growth makes fsync slow* (NTFS metadata): **rejected.**
   Appending, a pre-sized file and a pre-written file give the same
   distribution, so preallocating the journal (a recovery-format change)
   would buy nothing.
2. *Load causes the long fsyncs*: **half right.** A concurrent writer
   doubles the median (1.2 → 2.2 ms: a consumer NVMe drive flushes its
   whole shared write cache), but the worst burst of the lab, 3 fsyncs over
   100 ms, happened with the machine **idle**. The tail belongs to the
   device and the OS.

Consequences:

- **Group commit's real value here is stall recovery.** After a stall, the
  backlog shares fsyncs (up to 64 per fsync) and clears in milliseconds.
  With one fsync per order the backlog drains one fsync per request while
  new arrivals keep coming, so stalls compound.
- **The intake queue is a policy, not a fix.** The re-run with
  `CEX_QUEUE_SIZE=8192` had **zero** refusals at every rate (3200/s: p50
  1.98 ms, p99 10.22 ms in that run), while a stall in its 1600/s stage
  showed as waiting instead (p99 424 ms). A bigger queue turns 503s into
  latency during a stall; a smaller one fails fast and lets clients retry
  with the same Idempotency-Key. 1024 stays the default; the choice is
  written down.
- **Production hardware matters more than code here.** Datacenter SSDs
  with power-loss protection can acknowledge a flush from protected cache,
  making fsync short and predictable. That is a deployment requirement, not
  something a Go change can buy.

## Result 4: the one optimization

Of the owner goroutine's small CPU costs, one was pure overhead: two
`GetMetricWithLabelValues` lookups per command (hash the labels, take a read
lock) in the metrics observer. Commit `2ff20fd` resolves them once at
startup.

| | before | after |
|---|---|---|
| `BenchmarkApplied` (benchstat, n=8, interleaved) | 221.9 ns ± 21 % | **61.0 ns ± 7 % (−72.5 %, p = 0.000)** |
| allocations per call | 0 | 0 |
| in the 3200/s CPU profile | 0.05 s of 18 s | 0.01 s of 18 s |
| end-to-end step run | [batch64](../evidence/load/2026-10-27-step-batch64/report.md) | [after-observer](../evidence/load/2026-10-27-step-batch64-after-observer/report.md) |

**End to end it is not measurable**, and the report says so instead of
claiming a win: the two step runs differ far more by where the fsync stalls
landed (worst stage 800/s before, 3200/s after with 2,019 refusals) than by
160 ns per command. It is kept because it is simple, safe and removes a
lock from the serial path. Regressions: none attributable to the change.
The after-run's 3200/s stage was worse (2,019 refusals, p99 758 ms), and
its mean fsync was 1.395 ms against 1.201 ms in the stage before: about
1.9 s of extra fsync time in 20 s, when 1024 queue slots last only 0.32 s
at that rate. The before-run had its bad stage at 800/s instead (mean fsync
1.652 ms against ~1.47 ms around it).

## Result 5: 100 orders/s for 10 minutes

| | |
|---|---|
| p50 / p95 / p99 / p99.9 / max | 2.97 / 4.33 / **45.27** / 147.93 / 267.39 ms |
| errors | 0 of 60,000 |
| engine total p50 / p99 | 1.600 / 42.779 ms |
| fsyncs over 100 ms | 5 of 61,826 |

- **Hypothesis "p99 < 50 ms at 100/s": met in this run (45.3 ms), not
  robustly.** A discarded earlier run of the same code (marked `-dirty`)
  measured 248 ms, because its stalls were worse. The budget holds at the
  median by a factor of 17 and fails or passes at p99 depending on the
  disk's bad moments.
- **Memory grows linearly with commands**: heap 50 MB and RSS 66 MiB after
  60,000 commands, starting from 18 MiB. A heap profile
  (`/debug/pprof/heap` after 22,100 commands) attributes the live growth to
  one line, `s.dedupe[dk] = dedupeEntry{...}` in `state.Apply`: about
  **370 bytes per command**, kept forever. That is ADR-0004's Tier 1 choice
  (every idempotency key is retained), now with a number: ~133 MB of live
  heap per hour at 100/s (RSS roughly twice that, with GOGC=100). Dedupe
  expiry, which needs a documented retry window and replayed expiry
  commands, moves up the Tier 2 list.

## Result 6: database outage and slow viewers under load (Docker)

Compose stack, 200 orders/s, PostgreSQL frozen with `docker pause` for
30 s, 5 WebSocket viewers reading one message every 5 s
([timeline](../evidence/load/2026-10-27-outage-under-load/timeline.csv)).

| | before freeze | during | after |
|---|---|---|---|
| answered / s | 200 | 200 | 200 |
| errors | 0 | 0 | 0 |
| client p50 / p99 (ms) | 4.90 / 789.83 | 6.27 / 1513.98 | 4.96 / 1169.93 |
| max projection lag (commands) | 64 | 7,749 | 8,543 → 13 at the end |

- **Trading never waited for the database.** The applied seq advanced at
  200/s throughout (1,000 per 5 s, give or take a stall's backlog). The
  latency tail is the Docker VM's fsync: p50 3-4 ms there against 1.2 ms
  on the host, 16 fsyncs over 250 ms and one over 1 s in this run, with
  queue spikes before (t = 30 s), during and after (t = 105 s) the freeze.
- **The read model lagged by exactly the arrival rate and caught up within
  ~10 s** of the unfreeze (7,744 → ~0), with zero failures and nothing
  lost or doubled (projected 26,087 of 26,100 at the end, still following).
- **Finding: the projector stayed "healthy" the whole time.** A frozen
  database returns no error, it just hangs, so `cex_projection_healthy`
  and `cex_projection_failures_total` never changed; only the lag showed
  the outage. Follow-up: a statement timeout in the projector, so a hang
  becomes a counted failure. Meanwhile the lag alert is the one that fires.
- **Slow viewers**: the hub dropped all 5 within seconds
  (`cex_marketdata_slow_drops_total` = 5) while trading continued. The
  viewers themselves observed 0 disconnects in 2 minutes: a slow client
  only reads the close frame after everything already buffered in its
  socket. Server-side counting is what tells the truth.

## What this report does not claim

- Numbers from one laptop with the load generator on the same machine. They
  rank designs on this hardware; they are not capacity guarantees.
- One run per configuration. The fsync tail makes single-run p99s noisy; a
  proper comparison of small changes needs repeated runs and confidence
  intervals.
- Docker Desktop's VM is not production Linux storage, and crash safety
  there was not re-tested this week.

## Reproduce

```bash
make up && make load-step               # against Compose (results in tmp/)
loadtest/outage-under-load.sh tmp/outage
go run ./labs/fsync-tail -n 10000 -rounds 2
```

For a local server and the exact flags used here, see
[`loadtest/README.md`](../loadtest/README.md).
