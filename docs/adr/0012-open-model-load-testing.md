# ADR-0012: Open-model load testing with server-side attribution

- **Status:** Accepted
- **Date:** 2026-10-27

## Context

The plan asks for "reproducible workloads", "an open arrival-rate workload",
reported dropped iterations, p50/p95/p99, errors and saturation, and a
comparison of fsync-per-order against group commit. The numbers must say
not only *how slow* but *where the time went*.

## Options considered

1. **k6** (JavaScript scripts, `constant-arrival-rate` executor). Mature,
   reports dropped iterations. But it is a second toolchain to install, it
   cannot read the server's histograms, and the accounts, order mix and
   idempotency keys would be written twice (once in JS, once in Go tests). ❌
   for now; a k6 script remains a fine cross-check later.
2. **`go test -bench`**. Excellent for components (`evidence/bench`), but a
   benchmark is a closed loop: it waits for each result before sending the
   next, so it measures throughput, not latency under a given arrival rate. ❌
   for this purpose.
3. **A small Go load generator, `cmd/loadgen`** ✅. It shares the module's
   types and test helpers, runs anywhere Go runs, and can scrape the
   server's own metrics during each stage.

## Decision

- **Workloads are versioned JSON files** (`loadtest/workloads/`): seed,
  users, order mix, price band, sizes, funding, warmup, stages, max in
  flight, timeout. Unknown fields are errors. A seeded PCG generator
  (`math/rand/v2`) makes the offered sequence identical on every run and
  platform.
- **Open model.** Arrival *i* of a stage is due at `t0 + i/rate`, computed
  from `t0` so sleep overshoot never accumulates. Latency is measured from
  the **scheduled** arrival with the high-resolution clock (ADR-0011).
  When `max_in_flight` is reached, the arrival is **dropped and counted**,
  never delayed.
- **Answered vs errors.** 200/404/409/422 are answers; 429, 5xx and no
  response are errors. Percentiles cover answers only, so a fast 503 can
  never improve a latency figure. Client percentiles are exact (every sample
  kept); server percentiles are histogram estimates (`histogram_quantile`
  logic on scrape deltas).
- **Server-side attribution.** The generator scrapes `/metrics` at each
  stage boundary and every second during a stage, and reports per stage:
  HTTP (answered) ⊃ engine total ⊃ queue wait + durable + apply, fsync,
  mean batch size, CPU cores, max queue length, refusals, goroutines, RSS,
  projection lag, WebSocket slow drops.
- **The environment is recorded, not typed:** server version, Go version,
  batch limit and queue capacity come from the server's metrics; the git
  commit (marked `-dirty` if uncommitted) from git; hardware from `-note`.
- **Raw results are kept:** `requests.csv.gz` (one row per arrival),
  `timeline.csv` (per second), `metrics-end.prom`, optional `cpu.pprof`.
  The Markdown report is generated from them, never edited.
- **Load-test accounts are local test credentials**, derived
  deterministically (`loadgen tokens`), and are the same ones the Compose
  stack is configured with (`deploy/cexd.env`, checked by a test).

## Consequences

- **Easier:** one command per experiment; results that carry their own
  provenance; a run against the wrong server is detectable (it happened:
  a stale server on the same port answered a "batch=1" run, and the
  recorded batch limit of 64 exposed it).
- **Harder:** the generator shares the machine with the server in these
  experiments, so near saturation they compete for CPU (recorded in every
  report). Server metrics are attributed to the stage in which the server
  *finished* a request, so stage boundaries blur by up to one timeout.
- **Evidence:** `cmd/loadgen` tests (nearest-rank percentiles,
  histogram-quantile interpolation, generator determinism and mix,
  workload validation, token compatibility with cexd's parser, an
  end-to-end run against a real in-process exchange checking every
  arrival is accounted for), and the runs in `evidence/load/`.
