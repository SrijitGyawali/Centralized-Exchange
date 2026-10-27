# Load test: steady-100 (batch=64)

The plan's first experiment: 100 orders per second for 10 minutes after a 30-second warmup. Tests the hypothesis 'p99 under 50 ms at 100 orders/s' and shows whether anything drifts over time (memory, book depth, latency).

| | |
|---|---|
| started (UTC) | 2026-10-27T13:53:58Z |
| git commit | `b3c2d12ce13a` |
| machine | Windows 11 laptop, Intel i7-1165G7 (4 cores / 8 threads), 7.7 GiB RAM, KIOXIA 512 GB NVMe SSD (NTFS), on AC power; loadgen on the same machine, windows/amd64, 8 logical CPUs |
| server | b3c2d12 (go1.26.10), batch limit 64, queue capacity 1024 |
| load generator | go1.26.10, same machine as the server: true |
| workload | 50 users, mix rest/cross/cancel = 45/35/20, seed 2, max in flight 1000, timeout 5s |

## Client view: latency from the scheduled arrival (exact percentiles, ms)

Answered = 200/404/409/422 (the engine decided). Errors = 429, 5xx, no response. Dropped = never sent (max in flight reached).

| stage | offered/s | answered/s | dropped | errors | error rate | p50 | p95 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|---|---|---|---|
| warmup | 100 | 100 | 0 | 0 | 0.00% | 2.48 | 67.40 | 104.34 | 129.73 | 145.41 |
| 1: 100/s | 100 | 100 | 0 | 0 | 0.00% | 2.97 | 4.33 | 45.27 | 147.93 | 267.39 |

## Status codes

| stage | 200 | 404 | 409 | 422 | 429 | 503 | 504 | no response |
|---|---|---|---|---|---|---|---|---|
| warmup | 2399 | 601 | 0 | 0 | 0 | 0 | 0 | 0 |
| 1: 100/s | 47993 | 12007 | 0 | 0 | 0 | 0 | 0 | 0 |

## Server view: where the time went (histogram estimates, ms)

Each level contains the next: HTTP ⊃ engine total ⊃ queue wait + durable + apply. HTTP covers answered order requests only (refusals excluded). Engine rows cover every applied command (refused requests never reach the engine). Durable is per batch; batch = mean commands per fsync.

| stage | HTTP p50 / p99 | engine total p50 / p99 | queue wait p50 / p99 | durable p50 / p99 | batch | apply p50 / p99 | fsync p50 / p99 |
|---|---|---|---|---|---|---|---|
| warmup | 1.749 / 125.042 | 1.637 / 125.042 | 0.029 / 48.798 | 1.481 / 50.204 | 1.1 | 0.035 / 0.166 | 1.422 / 50.204 |
| 1: 100/s | 1.712 / 43.237 | 1.600 / 42.779 | 0.026 / 15.924 | 1.506 / 9.271 | 1 | 0.038 / 0.171 | 1.434 / 9.175 |

## Saturation

| stage | CPU cores used | max queue / capacity | refused (overloaded) | max goroutines | max RSS MiB | max projection lag | WS slow drops |
|---|---|---|---|---|---|---|---|
| warmup | 0.11 | 2 / 1024 | 0 | 35 | 23.2 | 0 | 0 |
| 1: 100/s | 0.12 | 7 / 1024 | 0 | 44 | 72.8 | 0 | 0 |

## Raw files

- `results.json`: every number above, plus the workload
- `requests.csv.gz`: one row per arrival (stage, kind, scheduled time, latency, status, dropped)
- `timeline.csv`: the server's gauges and counters, sampled every second
- `metrics-end.prom`: the server's full /metrics at the end of the run
