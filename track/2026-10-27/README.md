# 27 October 2026: measuring the exchange (week 7)

> **One-line summary:** the exchange can now be **measured**. Every request
> is timed at three nested levels, a load generator offers realistic
> traffic and asks the server where the time went, and one command starts
> the whole stack with dashboards. The measurements found three surprises:
> a clock too coarse to measure with, 17 vulnerabilities in the Go toolchain,
> and a disk whose rare slow fsyncs, not the code, set the latency tail.

Plan reference: week 7, "operational visibility". Gate: *the report
distinguishes engine time from durable latency and HTTP latency, includes
errors and saturation, and links raw results.* The report:
[docs/PERFORMANCE.md](../../docs/PERFORMANCE.md). Decisions:
[ADR-0011](../../docs/adr/0011-metrics-and-private-admin-port.md) (metrics),
[ADR-0012](../../docs/adr/0012-open-model-load-testing.md) (load testing).

---

## 1. Three latencies, nested

```
client      loadgen: scheduled arrival → response
 └ HTTP     handler start → return                     cex_http_request_duration_seconds
    └ engine  admission → reply                        cex_engine_command_seconds
        ├ queue wait   waiting for the owner goroutine cex_engine_queue_wait_seconds
        ├ durable      write + fsync, ONE per batch    cex_engine_durable_seconds
        └ apply        the state machine + publish     cex_engine_apply_seconds
```

At 400 orders/s the medians were: apply 0.04 ms, durable 1.5 ms, engine
1.6 ms, HTTP 1.7 ms, client 2.8 ms. **Durability is ~95 % of the engine's
time.** The gap between two levels is a finding of its own (HTTP minus
engine = JSON, auth, routing; client minus HTTP = loopback networking and
the generator).

---

## 2. How the code reports without importing Prometheus

Only `internal/telemetry` imports the Prometheus client. Everyone else
declares a small hook and lets telemetry plug in:

| Package | Hook it declares | Why this shape |
|---|---|---|
| `engine` | `Observer` interface (6 methods) | The engine owns the contract; tests pass a 10-line fake |
| `journal` | `Options.OnSync func(bytes, write, sync)` | One method: a function field is simpler than an interface |
| `marketdata` | `Hub.Stats()` | Counters change under the hub's existing mutex; Stats returns a copy |
| `projector` | `Status.Failures()` | A counter, because a boolean can look healthy at every scrape |
| `api` | `Server.Route(r)` | The route PATTERN as the label, never the raw path |

Metrics live on a **private admin port** (`CEX_ADMIN_ADDR`, loopback by
default) together with `/debug/pprof`. A CPU profile costs CPU and a heap
profile can reveal memory: neither belongs on the public port.

---

## 3. Surprise 1: the ruler was broken

The first load test said apply took "0.001 ms at p50 and 0.75 ms at p99".
Impossible. A 20-line experiment: 199,999 of 200,000 back-to-back
`time.Now()` calls returned the same value; the smallest step was **578 µs**.
On Windows, Go's monotonic clock reads the kernel's interrupt time, which
only moves on timer ticks.

Fix: `internal/clock` reads `QueryPerformanceCounter` (100 ns) through the
standard `syscall` package on Windows, `time.Now` elsewhere. It costs ~90 ns
per reading against ~6 ns: a trade made on purpose. Lesson: **check the ruler
before trusting the measurement.**

---

## 4. Surprise 2: 17 vulnerabilities, in Go itself

`govulncheck` (now in CI) found 17 vulnerabilities in the go1.26.5 standard
library that our code can actually reach (net/http, crypto/tls, crypto/x509,
net/url, …). The fix was one line, `go 1.26.10` in go.mod, which also
selects the toolchain for everyone, including CI and the Docker build.
After that: 0.

---

## 5. The load generator (open model)

- **Arrivals on a schedule.** Arrival *i* is due at `t0 + i/rate` whether
  or not earlier requests finished. A closed loop (N workers waiting for
  replies) slows down with the server and hides the queueing:
  *coordinated omission*.
- **Latency from the scheduled time**, so lateness in the generator counts.
- **Dropped, not delayed**, when too many are in flight; counted.
- **Answered vs errors**: 200/404/409/422 are answers, 429/5xx are errors.
  A fast 503 must never make an overloaded stage look quick.
- **The server's own view** from `/metrics` at every stage boundary and
  every second; the environment (batch limit, version) read from the
  server, never typed.

That last point paid off at once: a "batch = 1" run showed batches of 5.
A stale batch-64 server from an earlier test still held the port; the new
one had refused to start. The recorded batch limit exposed the mislabel.

---

## 6. Results in one table

| Question | Answer | Evidence |
|---|---|---|
| Group commit vs fsync per order? | Batch 1 saturates at ~750/s (58-76 % refused beyond); batch 64 serves 3200/s at a 2.4 ms median | step runs |
| Where does the engine's time go? | ~93 % waiting in fsync (profile at 3200/s); CPU work under 1 s in 18 | `cpu.pprof` |
| Why the long p99s? | Rare fsyncs of 100 ms-1 s stall the single writer | fsync histograms |
| Is it the file growing? | **No.** Preallocating changed nothing (lab) | `labs/fsync-tail` |
| Is it our load? | Half: load doubles the median fsync, but the worst burst came with the machine idle | lab |
| p99 < 50 ms at 100/s? | 45.3 ms in the clean run; 248 ms in a discarded one. Depends on the disk's bad moments | steady run |
| Memory over time? | +370 B live per command, forever: the dedupe table (ADR-0004) | heap profile |
| Database frozen 30 s under load? | 0 errors, throughput unchanged, lag +7,744 then caught up in ~10 s | outage drill |
| Slow WebSocket viewers? | Dropped by the server within seconds; trading unaffected | outage drill |

**The one optimization** (the plan asks for exactly one): the metrics
observer resolved its labelled counters on every command (hash + lock).
Resolving them once made the call **221.9 → 61.0 ns (−72.5 %)**, and changed
nothing measurable end to end, because 160 ns is invisible next to a 1.3 ms
fsync and its stalls. Reporting "no measurable effect" is a result too.

---

## 7. Three things I would not have guessed

1. **Group commit's value here is recovering from stalls**, more than raw
   throughput. After a stall, 64 queued commands share one fsync and the
   backlog clears in milliseconds; with one fsync per order, stalls compound.
2. **A frozen database looks healthy.** `docker pause` makes connections
   hang, not fail, so the projector's health flag never changed. Only the
   lag showed the outage. (Follow-up: a statement timeout.)
3. **A slow client learns it was dropped late.** The hub closed all five
   slow viewers in seconds, but each still had a socket buffer full of
   older messages to read first, so after two minutes none had seen the close.

---

## 8. Go ideas met today

| Idea | Where | In one line |
|---|---|---|
| Consumer-declared interfaces | `engine.Observer` | The engine says what it reports, telemetry implements it |
| Null object | `nopObserver` | No `if obs != nil` on the hot path |
| Typed nil | `cexd` `watched` | A nil `*T` in an interface is NOT a nil interface |
| `prometheus.Collector` | `hubCollector` | One `Stats()` per scrape: consistent values |
| `promauto.With(reg)` | `telemetry` | Create and register in one step, on a private registry |
| ServeMux pattern conflicts | `admin.go` | `"/x/y"` vs `"GET /x/"` is ambiguous: Go panics at registration |
| `net/http/pprof` | admin port | Imported for its handlers, served only on an explicit mux |
| Build constraints | `clock_windows.go` | One implementation per platform, chosen at compile time |
| `syscall.NewLazyDLL` | `internal/clock` | Call a Windows API with no cgo and no module |
| `math/rand/v2` PCG | `loadgen` | A fully specified generator: same seed, same ops, any platform |
| Buffered channel as semaphore | `runner.go` | `select { case sem <- x: … default: drop }` |
| Keyed array literals | `telemetry` | `[n]string{kindFund: "fund"}`: index and label cannot drift |
| `package main` must link | `loadgen` | A main package without `func main` fails at link time |
| `GOTOOLCHAIN` / go line | go.mod | The go directive picks the compiler for everyone |

---

## 9. Try it yourself

```bash
make up                  # http://localhost:3000 for Grafana, :9090 Prometheus
make load-smoke          # tmp/load-smoke/report.md
make load-step           # 2.5 minutes, with a CPU profile
go tool pprof -top tmp/load-step/cpu.pprof
loadtest/outage-under-load.sh tmp/outage
go run ./labs/fsync-tail -n 10000 -rounds 2
make down
```

---

## 10. Check yourself

1. Why is latency measured from the *scheduled* arrival, and what goes
   wrong with a closed loop of workers?
2. Why can a per-batch p99 of 5 ms hide a client p95 of 600 ms?
3. What made `time.Now` useless for measuring apply time here, and how
   would you have noticed?
4. Why does group commit help most right *after* a stall?
5. Why is `CEX_QUEUE_SIZE` a policy choice rather than a performance fix?
6. Why did the projector report "healthy" during a database freeze?
7. Why is a route pattern a safe metric label and a raw path is not?
8. What retains ~370 bytes per command, and why was that a deliberate
   Tier 1 choice?

---

## 11. Next session (week 8)

**Freeze and demonstrate**: a replay-inspect CLI polish, quickstart,
architecture diagram, known-limitations page (this week added several:
dedupe memory, the fsync tail, the projector's hang blindness), a 3-minute
demo script, a clean-machine setup check, the crash and restore drills, and
the v0.1 tag.
