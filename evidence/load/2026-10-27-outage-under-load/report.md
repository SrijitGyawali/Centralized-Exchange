# Load test: outage-under-load (postgres paused 30s + 5 slow viewers)

Steady 200 orders/s for 2 minutes, during which loadtest/outage-under-load.sh freezes PostgreSQL for 30 seconds and slow WebSocket viewers keep getting disconnected. Trading latency must not change; projection lag must grow, then return to near zero.

| | |
|---|---|
| started (UTC) | 2026-10-27T14:04:42Z |
| git commit | `a384d9a52338` |
| machine | Windows 11 laptop, Intel i7-1165G7 (4 cores / 8 threads), 7.7 GiB RAM, KIOXIA 512 GB NVMe SSD; cexd and PostgreSQL in Docker Desktop (WSL2 VM, ext4 on a virtual disk); loadgen on the Windows host, windows/amd64, 8 logical CPUs |
| server | a384d9a (go1.26.10), batch limit 64, queue capacity 1024 |
| load generator | go1.26.10, same machine as the server: true |
| workload | 50 users, mix rest/cross/cancel = 45/35/20, seed 3, max in flight 1000, timeout 5s |

## Client view: latency from the scheduled arrival (exact percentiles, ms)

Answered = 200/404/409/422 (the engine decided). Errors = 429, 5xx, no response. Dropped = never sent (max in flight reached).

| stage | offered/s | answered/s | dropped | errors | error rate | p50 | p95 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|---|---|---|---|
| warmup | 200 | 200 | 0 | 0 | 0.00% | 6.21 | 35.36 | 78.14 | 111.27 | 119.03 |
| 1: 200/s | 200 | 200 | 0 | 0 | 0.00% | 4.90 | 548.48 | 789.83 | 969.90 | 989.90 |
| 2: 200/s | 200 | 200 | 0 | 0 | 0.00% | 6.27 | 1099.29 | 1513.98 | 1645.41 | 1669.04 |
| 3: 200/s | 200 | 200 | 0 | 0 | 0.00% | 4.96 | 68.12 | 1169.93 | 1658.02 | 1718.18 |

## Status codes

| stage | 200 | 404 | 409 | 422 | 429 | 503 | 504 | no response |
|---|---|---|---|---|---|---|---|---|
| warmup | 1593 | 407 | 0 | 0 | 0 | 0 | 0 | 0 |
| 1: 200/s | 3226 | 774 | 0 | 0 | 0 | 0 | 0 | 0 |
| 2: 200/s | 6357 | 1643 | 0 | 0 | 0 | 0 | 0 | 0 |
| 3: 200/s | 9537 | 2463 | 0 | 0 | 0 | 0 | 0 | 0 |

## Server view: where the time went (histogram estimates, ms)

Each level contains the next: HTTP ⊃ engine total ⊃ queue wait + durable + apply. HTTP covers answered order requests only (refusals excluded). Engine rows cover every applied command (refused requests never reach the engine). Durable is per batch; batch = mean commands per fsync.

| stage | HTTP p50 / p99 | engine total p50 / p99 | queue wait p50 / p99 | durable p50 / p99 | batch | apply p50 / p99 | fsync p50 / p99 |
|---|---|---|---|---|---|---|---|
| warmup | 4.591 / 47.542 | 4.508 / 47.507 | 0.040 / 24.511 | 4.036 / 23.013 | 1.1 | 0.049 / 0.435 | 4.008 / 22.982 |
| 1: 200/s | 3.835 / 876.419 | 3.591 / 875.617 | 0.032 / 423.346 | 3.035 / 18.918 | 1.1 | 0.039 / 0.171 | 2.966 / 18.796 |
| 2: 200/s | 4.591 / 2212.324 | 4.534 / 2212.324 | 0.040 / 1949.751 | 3.939 / 24.608 | 1.3 | 0.051 / 0.246 | 3.900 / 24.600 |
| 3: 200/s | 4.009 / 943.006 | 3.835 / 940.220 | 0.034 / 726.351 | 3.318 / 20.431 | 1.1 | 0.041 / 0.245 | 3.242 / 20.352 |

## Saturation

| stage | CPU cores used | max queue / capacity | refused (overloaded) | max goroutines | max RSS MiB | max projection lag | WS slow drops |
|---|---|---|---|---|---|---|---|
| warmup | 0.35 | 8 / 1024 | 0 | 67 | 25.5 | 15 | 0 |
| 1: 200/s | 0.21 | 106 / 1024 | 0 | 355 | 36.3 | 64 | 5 |
| 2: 200/s | 0.23 | 227 / 1024 | 0 | 640 | 53.5 | 7749 | 0 |
| 3: 200/s | 0.25 | 172 / 1024 | 0 | 578 | 68.8 | 8543 | 0 |

## Slow WebSocket viewers

5 viewers reading one message every 5s: 5 connections, 0 disconnects observed.

## Raw files

- `results.json`: every number above, plus the workload
- `requests.csv.gz`: one row per arrival (stage, kind, scheduled time, latency, status, dropped)
- `timeline.csv`: the server's gauges and counters, sampled every second
- `metrics-end.prom`: the server's full /metrics at the end of the run
