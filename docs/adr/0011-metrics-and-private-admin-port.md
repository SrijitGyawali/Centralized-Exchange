# ADR-0011: Metrics, the private admin port, and a clock fine enough to measure with

- **Status:** Accepted
- **Date:** 2026-10-27

## Context

Week 7 asks for operational visibility. The gate: *the report distinguishes
engine time from durable latency and HTTP latency, includes errors and
saturation, and links raw results.* Until now the only window into the
running server was its logs and `/v1/status`.

Three questions had to be answered:

1. Which metrics library, and where may it be imported?
2. Where are metrics and profiling served?
3. Can the numbers be trusted?

## Options considered

**Library**

1. **Hand-written Prometheus text format.** The format is simple, but
   histograms (cumulative buckets, `+Inf`, `_sum`/`_count`), escaping,
   content negotiation and the Go runtime collectors are easy to get subtly
   wrong. Too much distraction for little learning. ❌
2. **OpenTelemetry metrics SDK.** Vendor-neutral, but a much larger
   dependency tree and more concepts (meter providers, exporters, views) than
   a single process needs. ❌
3. **`prometheus/client_golang`** ✅. The reference implementation, with
   runtime and process collectors, `promhttp`, `testutil`, and the same
   text parser Prometheus uses. ADR-0003 rule 2: building it would be a
   large distraction.

**Where it may be imported:** only in `internal/telemetry`. The engine,
journal, hub and projector report through small interfaces or plain
functions that they declare themselves (ADR-0003 rule 4, "keep every module
at the edge"):

| Producer | Hook it declares | Measures |
|---|---|---|
| `engine` | `Observer` interface | queue wait, durable append (per batch), apply, total, queries, refusals |
| `journal` | `Options.OnSync func(bytes, write, sync)` | write vs fsync time, bytes |
| `marketdata` | `Hub.Stats()` | subscribers, published, delivered, bytes, slow drops, refusals |
| `projector` | `Status.Failures()` | failed starts/catch-ups |
| `api` | `Server.Route(r)` | the route pattern used as the HTTP label |

**Where it is served**

1. On the public API port (`:8080/metrics`). Simple, but `/debug/pprof`
   must never be public: a CPU profile costs CPU, a heap profile can reveal
   memory contents. ❌
2. **A second, private listener, `CEX_ADMIN_ADDR`** ✅, default
   `127.0.0.1:6060` (loopback only). It serves `/metrics` and
   `/debug/pprof/*` and nothing else. Compose binds it inside the
   container network and publishes it on the host's loopback only; `off`
   disables it.

## Decision

- **The three latencies nest**, and each has its own histogram:
  `cex_http_request_duration_seconds` ⊃ `cex_engine_command_seconds` ⊃
  `cex_engine_queue_wait_seconds` + `cex_engine_durable_seconds` +
  `cex_engine_apply_seconds`. The gap between two levels is itself a
  finding (HTTP minus engine = JSON, auth, scheduling).
- **Saturation:** queue length vs capacity (read at scrape time), refusals
  by reason, CPU, goroutines, scheduler latency. **Errors:** HTTP 5xx by
  route; business rejections are counted as *outcomes*, not errors.
- **Labels are closed vocabularies.** Routes are ServeMux patterns
  (`GET /v1/orders/{id}`), never raw paths; anything unmatched is
  `unmatched`. Known label combinations are created at zero so `rate()`
  and alerts see them before the first event.
- **Buckets are tuned to measured values:** 1 µs–10 ms for apply,
  dense around the ~1.2 ms fsync for disk, 50 µs–10 s for waits.
- **The WebSocket stream is not timed** as an HTTP request (it lives for
  hours); the hub has its own metrics.
- **A private registry**, not the global default: no collisions between
  servers in one test process, nothing leaked in by dependencies.
- **`cexd healthcheck`** asks `/readyz` from inside the container, because
  the distroless image has no shell or curl.

### The clock

Measuring revealed that on Windows, Go's `time.Now` advances only on timer
interrupts: **every 578 µs** on the development machine. Every apply (a
few µs) read as 0 or 578 µs, and fsync times were rounded to multiples of
0.58 ms. `internal/clock` therefore reads `QueryPerformanceCounter` on
Windows (100 ns resolution, through the standard `syscall` package) and
`time.Now` elsewhere (already nanosecond-precise on Linux). It costs
~90 ns per reading on Windows against ~6 ns for `time.Now`; at three or
four readings per command that is well under 1 µs, accepted for numbers that
mean something. It is used only for measuring, never by the state machine.

## Consequences

- **Easier:** every claim in the performance report can be checked on a
  dashboard; operators get alerts with an action attached; profiling a
  live server is one URL away (on loopback).
- **Harder:** one more dependency (and its transitive modules) to keep
  patched: `govulncheck` now runs in CI. A second port to secure in any
  real deployment. Each command pays a few hundred nanoseconds for
  measurement; benchmarked, not assumed.
- **Evidence:** `internal/telemetry` tests (promlint naming rules, the
  text format, a real engine's counters, HTTP labels, the hub collector,
  projection gauges), `cmd/cexd` tests (metrics and pprof only on the admin
  port, admin can be off, healthcheck), `internal/clock` tests (resolution,
  monotonicity) and benchmarks.
