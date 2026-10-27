# Load test: step-to-saturation (batch=64 CEX_QUEUE_SIZE=8192)

Doubles the offered order rate every 20 seconds, from 100/s to 3200/s, to find where latency bends and where the server starts refusing work. Run once with CEX_BATCH_SIZE=1 and once with the default 64 to compare fsync-per-order with group commit.

| | |
|---|---|
| started (UTC) | 2026-10-27T13:58:33Z |
| git commit | `a384d9a52338` |
| machine | Windows 11 laptop, Intel i7-1165G7 (4 cores / 8 threads), 7.7 GiB RAM, KIOXIA 512 GB NVMe SSD (NTFS), on AC power; loadgen on the same machine, windows/amd64, 8 logical CPUs |
| server | a384d9a (go1.26.10), batch limit 64, queue capacity 8192 |
| load generator | go1.26.10, same machine as the server: true |
| workload | 50 users, mix rest/cross/cancel = 45/35/20, seed 1, max in flight 2000, timeout 5s |

## Client view: latency from the scheduled arrival (exact percentiles, ms)

Answered = 200/404/409/422 (the engine decided). Errors = 429, 5xx, no response. Dropped = never sent (max in flight reached).

| stage | offered/s | answered/s | dropped | errors | error rate | p50 | p95 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|---|---|---|---|
| warmup | 100 | 100 | 0 | 0 | 0.00% | 3.17 | 170.27 | 270.80 | 350.08 | 359.89 |
| 1: 100/s | 100 | 100 | 0 | 0 | 0.00% | 2.48 | 4.14 | 45.60 | 78.15 | 97.38 |
| 2: 200/s | 200 | 200 | 0 | 0 | 0.00% | 2.78 | 3.52 | 3.98 | 6.92 | 11.03 |
| 3: 400/s | 400 | 400 | 0 | 0 | 0.00% | 1.02 | 2.75 | 23.53 | 92.64 | 106.77 |
| 4: 800/s | 800 | 800 | 0 | 0 | 0.00% | 2.37 | 7.62 | 66.65 | 97.22 | 106.83 |
| 5: 1600/s | 1600 | 1600 | 0 | 0 | 0.00% | 2.40 | 320.51 | 424.21 | 460.32 | 469.83 |
| 6: 3200/s | 3200 | 3200 | 0 | 0 | 0.00% | 1.98 | 2.99 | 10.22 | 63.43 | 77.38 |

## Status codes

| stage | 200 | 404 | 409 | 422 | 429 | 503 | 504 | no response |
|---|---|---|---|---|---|---|---|---|
| warmup | 808 | 192 | 0 | 0 | 0 | 0 | 0 | 0 |
| 1: 100/s | 1613 | 387 | 0 | 0 | 0 | 0 | 0 | 0 |
| 2: 200/s | 3186 | 814 | 0 | 0 | 0 | 0 | 0 | 0 |
| 3: 400/s | 6388 | 1612 | 0 | 0 | 0 | 0 | 0 | 0 |
| 4: 800/s | 12916 | 3084 | 0 | 0 | 0 | 0 | 0 | 0 |
| 5: 1600/s | 25647 | 6353 | 0 | 0 | 0 | 0 | 0 | 0 |
| 6: 3200/s | 51219 | 12781 | 0 | 0 | 0 | 0 | 0 | 0 |

## Server view: where the time went (histogram estimates, ms)

Each level contains the next: HTTP ⊃ engine total ⊃ queue wait + durable + apply. HTTP covers answered order requests only (refusals excluded). Engine rows cover every applied command (refused requests never reach the engine). Durable is per batch; batch = mean commands per fsync.

| stage | HTTP p50 / p99 | engine total p50 / p99 | queue wait p50 / p99 | durable p50 / p99 | batch | apply p50 / p99 | fsync p50 / p99 |
|---|---|---|---|---|---|---|---|
| warmup | 1.824 / 321.607 | 1.712 / 321.607 | 0.038 / 181.818 | 1.429 / 96.833 | 1.4 | 0.027 / 0.190 | 1.381 / 96.833 |
| 1: 100/s | 1.632 / 20.313 | 1.451 / 19.000 | 0.026 / 7.000 | 1.420 / 3.657 | 1 | 0.036 / 0.214 | 1.372 / 3.657 |
| 2: 200/s | 1.697 / 2.910 | 1.576 / 2.636 | 0.025 / 0.050 | 1.480 / 2.295 | 1 | 0.037 / 0.178 | 1.417 / 2.114 |
| 3: 400/s | 1.562 / 7.885 | 1.420 / 6.667 | 0.026 / 2.286 | 1.377 / 2.894 | 1 | 0.031 / 0.165 | 1.331 / 2.824 |
| 4: 800/s | 1.893 / 72.323 | 1.802 / 71.931 | 0.467 / 24.735 | 1.230 / 2.063 | 1.1 | 0.018 / 0.095 | 1.204 / 1.998 |
| 5: 1600/s | 1.983 / 463.291 | 1.921 / 463.291 | 0.710 / 459.019 | 1.144 / 3.096 | 2.2 | 0.007 / 0.065 | 1.137 / 2.929 |
| 6: 3200/s | 1.806 / 8.188 | 1.755 / 7.736 | 0.593 / 4.843 | 1.131 / 1.842 | 3.7 | 0.004 / 0.025 | 1.125 / 1.787 |

## Saturation

| stage | CPU cores used | max queue / capacity | refused (overloaded) | max goroutines | max RSS MiB | max projection lag | WS slow drops |
|---|---|---|---|---|---|---|---|
| warmup | 0.09 | 11 / 8192 | 0 | 58 | 22.9 | 0 | 0 |
| 1: 100/s | 0.12 | 7 / 8192 | 0 | 51 | 25.9 | 0 | 0 |
| 2: 200/s | 0.2 | 0 / 8192 | 0 | 49 | 28.5 | 0 | 0 |
| 3: 400/s | 0.35 | 8 / 8192 | 0 | 63 | 34.3 | 0 | 0 |
| 4: 800/s | 0.45 | 16 / 8192 | 0 | 133 | 47.9 | 0 | 0 |
| 5: 1600/s | 0.44 | 588 / 8192 | 0 | 1355 | 92.6 | 0 | 0 |
| 6: 3200/s | 0.6 | 4 / 8192 | 0 | 773 | 140.8 | 0 | 0 |

## Raw files

- `results.json`: every number above, plus the workload
- `requests.csv.gz`: one row per arrival (stage, kind, scheduled time, latency, status, dropped)
- `timeline.csv`: the server's gauges and counters, sampled every second
- `metrics-end.prom`: the server's full /metrics at the end of the run
