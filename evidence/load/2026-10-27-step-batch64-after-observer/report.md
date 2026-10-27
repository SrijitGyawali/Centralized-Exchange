# Load test: step-to-saturation (batch=64)

Doubles the offered order rate every 20 seconds, from 100/s to 3200/s, to find where latency bends and where the server starts refusing work. Run once with CEX_BATCH_SIZE=1 and once with the default 64 to compare fsync-per-order with group commit.

| | |
|---|---|
| started (UTC) | 2026-10-27T13:56:16Z |
| git commit | `a384d9a52338` |
| machine | Windows 11 laptop, Intel i7-1165G7 (4 cores / 8 threads), 7.7 GiB RAM, KIOXIA 512 GB NVMe SSD (NTFS), on AC power; loadgen on the same machine, windows/amd64, 8 logical CPUs |
| server | a384d9a (go1.26.10), batch limit 64, queue capacity 1024 |
| load generator | go1.26.10, same machine as the server: true |
| workload | 50 users, mix rest/cross/cancel = 45/35/20, seed 1, max in flight 2000, timeout 5s |

## Client view: latency from the scheduled arrival (exact percentiles, ms)

Answered = 200/404/409/422 (the engine decided). Errors = 429, 5xx, no response. Dropped = never sent (max in flight reached).

| stage | offered/s | answered/s | dropped | errors | error rate | p50 | p95 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|---|---|---|---|
| warmup | 100 | 100 | 0 | 0 | 0.00% | 1.98 | 7.92 | 83.94 | 181.19 | 182.87 |
| 1: 100/s | 100 | 100 | 0 | 0 | 0.00% | 2.41 | 29.27 | 125.93 | 182.14 | 202.18 |
| 2: 200/s | 200 | 200 | 0 | 0 | 0.00% | 2.53 | 4.28 | 18.25 | 46.23 | 61.26 |
| 3: 400/s | 400 | 400 | 0 | 0 | 0.00% | 2.52 | 3.71 | 62.49 | 230.86 | 250.89 |
| 4: 800/s | 800 | 800 | 0 | 0 | 0.00% | 2.24 | 36.18 | 50.20 | 82.24 | 107.29 |
| 5: 1600/s | 1600 | 1600 | 0 | 0 | 0.00% | 2.05 | 3.73 | 25.29 | 123.76 | 217.38 |
| 6: 3200/s | 3200 | 3099.1 | 0 | 2019 | 3.15% | 2.68 | 55.36 | 757.51 | 898.78 | 908.16 |

## Status codes

| stage | 200 | 404 | 409 | 422 | 429 | 503 | 504 | no response |
|---|---|---|---|---|---|---|---|---|
| warmup | 808 | 192 | 0 | 0 | 0 | 0 | 0 | 0 |
| 1: 100/s | 1613 | 387 | 0 | 0 | 0 | 0 | 0 | 0 |
| 2: 200/s | 3186 | 814 | 0 | 0 | 0 | 0 | 0 | 0 |
| 3: 400/s | 6388 | 1612 | 0 | 0 | 0 | 0 | 0 | 0 |
| 4: 800/s | 12916 | 3084 | 0 | 0 | 0 | 0 | 0 | 0 |
| 5: 1600/s | 25647 | 6353 | 0 | 0 | 0 | 0 | 0 | 0 |
| 6: 3200/s | 49602 | 12379 | 0 | 0 | 0 | 2019 | 0 | 0 |

## Server view: where the time went (histogram estimates, ms)

Each level contains the next: HTTP ⊃ engine total ⊃ queue wait + durable + apply. HTTP covers answered order requests only (refusals excluded). Engine rows cover every applied command (refused requests never reach the engine). Durable is per batch; batch = mean commands per fsync.

| stage | HTTP p50 / p99 | engine total p50 / p99 | queue wait p50 / p99 | durable p50 / p99 | batch | apply p50 / p99 | fsync p50 / p99 |
|---|---|---|---|---|---|---|---|
| warmup | 1.420 / 20.008 | 1.333 / 18.832 | 0.026 / 4.857 | 1.320 / 14.000 | 1 | 0.027 / 0.150 | 1.249 / 13.000 |
| 1: 100/s | 1.699 / 171.053 | 1.546 / 171.053 | 0.027 / 59.548 | 1.458 / 20.191 | 1 | 0.035 / 0.217 | 1.405 / 19.504 |
| 2: 200/s | 1.666 / 9.400 | 1.532 / 8.810 | 0.026 / 3.116 | 1.452 / 5.277 | 1 | 0.035 / 0.214 | 1.394 / 4.971 |
| 3: 400/s | 1.535 / 8.036 | 1.398 / 7.398 | 0.026 / 3.640 | 1.367 / 2.594 | 1 | 0.030 / 0.124 | 1.326 / 2.498 |
| 4: 800/s | 1.818 / 49.852 | 1.729 / 49.765 | 0.417 / 23.662 | 1.209 / 1.986 | 1.2 | 0.017 / 0.090 | 1.188 / 1.976 |
| 5: 1600/s | 1.974 / 24.323 | 1.902 / 23.576 | 0.639 / 10.336 | 1.168 / 2.220 | 2 | 0.009 / 0.082 | 1.158 / 1.994 |
| 6: 3200/s | 2.272 / 769.405 | 2.141 / 769.405 | 0.768 / 757.883 | 1.248 / 2.778 | 4.7 | 0.008 / 0.084 | 1.215 / 2.660 |

## Saturation

| stage | CPU cores used | max queue / capacity | refused (overloaded) | max goroutines | max RSS MiB | max projection lag | WS slow drops |
|---|---|---|---|---|---|---|---|
| warmup | 0.1 | 0 / 1024 | 0 | 27 | 72.5 | 0 | 0 |
| 1: 100/s | 0.12 | 2 / 1024 | 0 | 42 | 74.2 | 0 | 0 |
| 2: 200/s | 0.22 | 0 / 1024 | 0 | 37 | 75.7 | 0 | 0 |
| 3: 400/s | 0.35 | 5 / 1024 | 0 | 117 | 77.2 | 0 | 0 |
| 4: 800/s | 0.43 | 13 / 1024 | 0 | 143 | 89.7 | 0 | 0 |
| 5: 1600/s | 0.54 | 3 / 1024 | 0 | 190 | 94.9 | 0 | 0 |
| 6: 3200/s | 1.03 | 1024 / 1024 | 2019 | 2171 | 170.9 | 0 | 0 |

## Raw files

- `results.json`: every number above, plus the workload
- `requests.csv.gz`: one row per arrival (stage, kind, scheduled time, latency, status, dropped)
- `timeline.csv`: the server's gauges and counters, sampled every second
- `metrics-end.prom`: the server's full /metrics at the end of the run
- `cpu.pprof`: CPU profile (`go tool pprof -top cpu.pprof`)
