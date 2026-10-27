# Load test: step-to-saturation (batch=64)

Doubles the offered order rate every 20 seconds, from 100/s to 3200/s, to find where latency bends and where the server starts refusing work. Run once with CEX_BATCH_SIZE=1 and once with the default 64 to compare fsync-per-order with group commit.

| | |
|---|---|
| started (UTC) | 2026-10-27T13:41:03Z |
| git commit | `b3c2d12ce13a` |
| machine | Windows 11 laptop, Intel i7-1165G7 (4 cores / 8 threads), 7.7 GiB RAM, KIOXIA 512 GB NVMe SSD (NTFS), on AC power; loadgen on the same machine, windows/amd64, 8 logical CPUs |
| server | b3c2d12 (go1.26.10), batch limit 64, queue capacity 1024 |
| load generator | go1.26.10, same machine as the server: true |
| workload | 50 users, mix rest/cross/cancel = 45/35/20, seed 1, max in flight 2000, timeout 5s |

## Client view: latency from the scheduled arrival (exact percentiles, ms)

Answered = 200/404/409/422 (the engine decided). Errors = 429, 5xx, no response. Dropped = never sent (max in flight reached).

| stage | offered/s | answered/s | dropped | errors | error rate | p50 | p95 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|---|---|---|---|
| warmup | 100 | 100 | 0 | 0 | 0.00% | 2.31 | 5.72 | 17.77 | 33.68 | 41.00 |
| 1: 100/s | 100 | 100 | 0 | 0 | 0.00% | 2.83 | 10.62 | 145.17 | 198.94 | 254.42 |
| 2: 200/s | 200 | 200 | 0 | 0 | 0.00% | 2.67 | 3.38 | 3.83 | 14.16 | 23.48 |
| 3: 400/s | 400 | 400 | 0 | 0 | 0.00% | 2.82 | 3.59 | 7.67 | 19.98 | 33.57 |
| 4: 800/s | 800 | 772.3 | 0 | 555 | 3.47% | 2.54 | 1350.69 | 1905.11 | 2034.54 | 2053.23 |
| 5: 1600/s | 1600 | 1600 | 0 | 0 | 0.00% | 2.72 | 4.31 | 13.58 | 46.26 | 65.39 |
| 6: 3200/s | 3200 | 3193.1 | 0 | 138 | 0.22% | 2.42 | 125.89 | 316.25 | 392.59 | 441.94 |

## Status codes

| stage | 200 | 404 | 409 | 422 | 429 | 503 | 504 | no response |
|---|---|---|---|---|---|---|---|---|
| warmup | 808 | 192 | 0 | 0 | 0 | 0 | 0 | 0 |
| 1: 100/s | 1613 | 387 | 0 | 0 | 0 | 0 | 0 | 0 |
| 2: 200/s | 3186 | 814 | 0 | 0 | 0 | 0 | 0 | 0 |
| 3: 400/s | 6388 | 1612 | 0 | 0 | 0 | 0 | 0 | 0 |
| 4: 800/s | 12458 | 2987 | 0 | 0 | 0 | 555 | 0 | 0 |
| 5: 1600/s | 25647 | 6353 | 0 | 0 | 0 | 0 | 0 | 0 |
| 6: 3200/s | 51099 | 12763 | 0 | 0 | 0 | 138 | 0 | 0 |

## Server view: where the time went (histogram estimates, ms)

Each level contains the next: HTTP ⊃ engine total ⊃ queue wait + durable + apply. HTTP covers answered order requests only (refusals excluded). Engine rows cover every applied command (refused requests never reach the engine). Durable is per batch; batch = mean commands per fsync.

| stage | HTTP p50 / p99 | engine total p50 / p99 | queue wait p50 / p99 | durable p50 / p99 | batch | apply p50 / p99 | fsync p50 / p99 |
|---|---|---|---|---|---|---|---|
| warmup | 1.472 / 16.262 | 1.358 / 15.468 | 0.026 / 3.000 | 1.357 / 7.600 | 1 | 0.030 / 0.245 | 1.294 / 7.600 |
| 1: 100/s | 1.743 / 89.286 | 1.607 / 78.000 | 0.027 / 37.500 | 1.494 / 14.617 | 1 | 0.038 / 0.393 | 1.433 / 13.783 |
| 2: 200/s | 1.704 / 2.966 | 1.634 / 2.813 | 0.025 / 0.050 | 1.585 / 2.365 | 1 | 0.037 / 0.100 | 1.481 / 1.999 |
| 3: 400/s | 1.688 / 6.761 | 1.606 / 6.646 | 0.026 / 2.538 | 1.530 / 2.684 | 1 | 0.037 / 0.100 | 1.446 / 2.395 |
| 4: 800/s | 2.323 / 2294.080 | 2.214 / 2294.080 | 0.742 / 2269.033 | 1.267 / 4.924 | 1.4 | 0.017 / 0.131 | 1.235 / 4.919 |
| 5: 1600/s | 2.112 / 10.347 | 1.995 / 9.935 | 0.691 / 5.674 | 1.206 / 2.285 | 2.1 | 0.011 / 0.082 | 1.184 / 2.171 |
| 6: 3200/s | 2.441 / 304.092 | 2.318 / 303.848 | 0.834 / 264.169 | 1.326 / 5.700 | 5.3 | 0.009 / 0.090 | 1.273 / 5.319 |

## Saturation

| stage | CPU cores used | max queue / capacity | refused (overloaded) | max goroutines | max RSS MiB | max projection lag | WS slow drops |
|---|---|---|---|---|---|---|---|
| warmup | 0.1 | 0 / 1024 | 0 | 17 | 71.1 | 0 | 0 |
| 1: 100/s | 0.13 | 1 / 1024 | 0 | 48 | 74.7 | 0 | 0 |
| 2: 200/s | 0.22 | 0 / 1024 | 0 | 34 | 76.6 | 0 | 0 |
| 3: 400/s | 0.41 | 2 / 1024 | 0 | 37 | 83.6 | 0 | 0 |
| 4: 800/s | 0.42 | 1024 / 1024 | 555 | 2205 | 151.4 | 0 | 0 |
| 5: 1600/s | 0.69 | 3 / 1024 | 0 | 1122 | 139.5 | 0 | 0 |
| 6: 3200/s | 1.06 | 341 / 1024 | 138 | 1627 | 171.9 | 0 | 0 |

## Raw files

- `results.json`: every number above, plus the workload
- `requests.csv.gz`: one row per arrival (stage, kind, scheduled time, latency, status, dropped)
- `timeline.csv`: the server's gauges and counters, sampled every second
- `metrics-end.prom`: the server's full /metrics at the end of the run
- `cpu.pprof`: CPU profile (`go tool pprof -top cpu.pprof`)
