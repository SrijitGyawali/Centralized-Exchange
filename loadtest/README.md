# loadtest/

Reproducible workloads for `cmd/loadgen` (week 7, ADR-0012). Results go to
`evidence/load/`, never edited by hand.

## Workloads

| File | What it asks | Use |
|---|---|---|
| [`workloads/smoke.json`](workloads/smoke.json) | 50 orders/s for 3 s, 5 users | Proves the tool and the metrics work (CI, `make load-smoke`). Its numbers mean nothing. |
| [`workloads/step.json`](workloads/step.json) | 100 → 3200 orders/s, doubling every 20 s | Finds the knee and the saturation point. Run with `CEX_BATCH_SIZE=1` and `64` to compare fsync-per-order with group commit. |
| [`workloads/steady.json`](workloads/steady.json) | 100 orders/s for 10 minutes | The plan's first experiment: tests "p99 < 50 ms at 100/s" and shows drift (memory, latency) over time. |
| [`workloads/outage.json`](workloads/outage.json) | 200 orders/s for 2 minutes | Driven by [`outage-under-load.sh`](outage-under-load.sh): PostgreSQL frozen for 30 s and slow WebSocket viewers, under load. |

Every workload fixes: seed, users, order mix (rest / cross / cancel),
price band, order sizes, funding, warmup, stages, max in flight and client
timeout. Same file and seed → same offered sequence of requests. (Cancel
targets depend on which orders actually rested, so the *responses* can
differ between runs; the *offered load* cannot.)

## Method (why the numbers can be trusted)

- **Open model.** Arrivals follow a fixed schedule (rate R = one every 1/R s)
  whether or not earlier requests have finished. A closed loop of N workers
  slows down when the server does and hides the queueing it should measure
  ("coordinated omission").
- **Latency from the scheduled arrival**, not from when the request was
  finally sent, using a high-resolution clock (`internal/clock`: Windows'
  `time.Now` only ticks every ~0.5 ms).
- **Dropped, not delayed.** When `max_in_flight` requests are outstanding,
  an arrival is dropped and counted. A run with drops did not offer its
  nominal rate, and the report says so.
- **Answered vs errors.** 200/404/409/422 are answers (the engine decided).
  429, 5xx and no response are errors. Latency percentiles are over answers,
  so fast 503 refusals can never make an overloaded stage look quick.
- **Exact percentiles** from every sample on the client; never averages of
  percentiles. Server-side numbers are histogram estimates (bucket bounds in
  `internal/telemetry`).
- **Generous funding**, so the test measures trading, not "insufficient
  funds" rejections.
- **Warmup** first, reported separately.
- **Fresh data directory** per run, so the journal and book start empty.

## What a run records

`loadgen run -out DIR` writes:

| File | Contents |
|---|---|
| `report.md` | Client view (offered vs answered rate, drops, errors, p50…max), status codes, server view (HTTP ⊃ engine total ⊃ queue wait + durable + apply, fsync, batch size), saturation (CPU, queue, refusals, goroutines, RSS, projection lag, WebSocket drops) |
| `results.json` | All of the above plus the workload and the environment: OS, CPUs, Go versions, git commit (with `-dirty` if uncommitted), server version, batch limit, queue capacity, `-note` |
| `requests.csv.gz` | One row per arrival: the raw data behind every client number |
| `timeline.csv` | The server's gauges and counters, sampled every second |
| `metrics-end.prom` | The server's full `/metrics` at the end |
| `cpu.pprof` | With `-profile-stage N`: a CPU profile taken during stage N |

The server's batch limit and queue size are read from its metrics, never
from the label you type, so a mislabelled run is detectable (this happened
once in week 7: a stale server on the same port answered instead).

## Running one

Against the Compose stack (it already has the load-test accounts):

```bash
make up
make load-smoke            # or: make load-step
```

Against a local server:

```bash
export CEX_API_TOKENS="$(go run ./cmd/loadgen tokens loadtest/workloads/step.json)"
CEX_DATA_DIR=$(mktemp -d) CEX_BATCH_SIZE=64 CEX_RATE_PER_SECOND=100000 CEX_RATE_BURST=100000 make run
# another terminal:
go run ./cmd/loadgen run -workload loadtest/workloads/step.json -out tmp/step \
  -note "describe the machine and disk" -profile-stage 6
```

Record the machine in `-note` (CPU, RAM, disk, power). The generator runs
on the same machine as the server unless you say otherwise
(`-same-machine=false`): it competes for CPU, which matters near saturation.

Targets such as "p99 < 50 ms at 100 orders/s" are hypotheses to test, not
results to quote.
