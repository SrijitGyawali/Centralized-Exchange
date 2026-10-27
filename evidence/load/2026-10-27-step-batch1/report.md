# Load test: step-to-saturation (batch=1)

Doubles the offered order rate every 20 seconds, from 100/s to 3200/s, to find where latency bends and where the server starts refusing work. Run once with CEX_BATCH_SIZE=1 and once with the default 64 to compare fsync-per-order with group commit.

| | |
|---|---|
| started (UTC) | 2026-10-27T13:43:22Z |
| git commit | `b3c2d12ce13a` |
| machine | Windows 11 laptop, Intel i7-1165G7 (4 cores / 8 threads), 7.7 GiB RAM, KIOXIA 512 GB NVMe SSD (NTFS), on AC power; loadgen on the same machine, windows/amd64, 8 logical CPUs |
| server | b3c2d12 (go1.26.10), batch limit 1, queue capacity 1024 |
| load generator | go1.26.10, same machine as the server: true |
| workload | 50 users, mix rest/cross/cancel = 45/35/20, seed 1, max in flight 2000, timeout 5s |

## Client view: latency from the scheduled arrival (exact percentiles, ms)

Answered = 200/404/409/422 (the engine decided). Errors = 429, 5xx, no response. Dropped = never sent (max in flight reached).

| stage | offered/s | answered/s | dropped | errors | error rate | p50 | p95 | p99 | p99.9 | max |
|---|---|---|---|---|---|---|---|---|---|---|
| warmup | 100 | 100 | 0 | 0 | 0.00% | 2.96 | 402.71 | 711.59 | 772.44 | 781.29 |
| 1: 100/s | 100 | 100 | 0 | 0 | 0.00% | 3.03 | 3.71 | 4.79 | 64.40 | 90.21 |
| 2: 200/s | 200 | 200 | 0 | 0 | 0.00% | 2.52 | 3.27 | 3.69 | 7.92 | 12.64 |
| 3: 400/s | 400 | 400 | 0 | 0 | 0.00% | 1.98 | 2.95 | 20.75 | 80.98 | 87.01 |
| 4: 800/s | 800 | 800 | 0 | 0 | 0.00% | 128.76 | 543.81 | 3828.43 | 3833.68 | 3833.99 |
| 5: 1600/s | 1600 | 675.4 | 0 | 18492 | 57.79% | 1181.22 | 2877.98 | 3978.58 | 4035.14 | 4040.73 |
| 6: 3200/s | 3200 | 757.9 | 0 | 48843 | 76.32% | 1258.51 | 2874.03 | 2917.36 | 2924.09 | 2924.95 |

## Status codes

| stage | 200 | 404 | 409 | 422 | 429 | 503 | 504 | no response |
|---|---|---|---|---|---|---|---|---|
| warmup | 808 | 192 | 0 | 0 | 0 | 0 | 0 | 0 |
| 1: 100/s | 1613 | 387 | 0 | 0 | 0 | 0 | 0 | 0 |
| 2: 200/s | 3186 | 814 | 0 | 0 | 0 | 0 | 0 | 0 |
| 3: 400/s | 6388 | 1612 | 0 | 0 | 0 | 0 | 0 | 0 |
| 4: 800/s | 12916 | 3084 | 0 | 0 | 0 | 0 | 0 | 0 |
| 5: 1600/s | 10714 | 2794 | 0 | 0 | 0 | 18492 | 0 | 0 |
| 6: 3200/s | 11249 | 3908 | 0 | 0 | 0 | 48843 | 0 | 0 |

## Server view: where the time went (histogram estimates, ms)

Each level contains the next: HTTP ⊃ engine total ⊃ queue wait + durable + apply. HTTP covers answered order requests only (refusals excluded). Engine rows cover every applied command (refused requests never reach the engine). Durable is per batch; batch = mean commands per fsync.

| stage | HTTP p50 / p99 | engine total p50 / p99 | queue wait p50 / p99 | durable p50 / p99 | batch | apply p50 / p99 | fsync p50 / p99 |
|---|---|---|---|---|---|---|---|
| warmup | 1.744 / 868.553 | 1.670 / 868.553 | 0.028 / 864.865 | 1.525 / 22.030 | 1 | 0.036 / 0.158 | 1.438 / 22.030 |
| 1: 100/s | 1.779 / 2.993 | 1.724 / 2.965 | 0.025 / 0.050 | 1.691 / 2.710 | 1 | 0.040 / 0.100 | 1.607 / 2.471 |
| 2: 200/s | 1.719 / 2.944 | 1.632 / 2.753 | 0.025 / 0.050 | 1.555 / 2.383 | 1 | 0.038 / 0.142 | 1.453 / 1.999 |
| 3: 400/s | 1.605 / 20.200 | 1.453 / 20.000 | 0.026 / 17.500 | 1.410 / 2.070 | 1 | 0.033 / 0.100 | 1.364 / 1.996 |
| 4: 800/s | 149.030 / 954.305 | 148.745 / 954.170 | 145.938 / 953.198 | 1.204 / 1.966 | 1 | 0.018 / 0.097 | 1.187 / 1.946 |
| 5: 1600/s | 1886.506 / 4838.257 | 1886.506 / 4838.257 | 1886.345 / 4838.096 | 1.143 / 14.173 | 1 | 0.013 / 0.071 | 1.138 / 14.120 |
| 6: 3200/s | 1799.190 / 4593.825 | 1799.190 / 4593.825 | 1799.141 / 4593.442 | 1.163 / 1.996 | 1 | 0.017 / 0.092 | 1.153 / 1.989 |

## Saturation

| stage | CPU cores used | max queue / capacity | refused (overloaded) | max goroutines | max RSS MiB | max projection lag | WS slow drops |
|---|---|---|---|---|---|---|---|
| warmup | 0.14 | 4 / 1024 | 0 | 92 | 23.4 | 0 | 0 |
| 1: 100/s | 0.17 | 0 / 1024 | 0 | 92 | 26.1 | 0 | 0 |
| 2: 200/s | 0.21 | 0 / 1024 | 0 | 93 | 28.4 | 0 | 0 |
| 3: 400/s | 0.36 | 15 / 1024 | 0 | 107 | 33.7 | 0 | 0 |
| 4: 800/s | 0.46 | 546 / 1024 | 0 | 1105 | 68.3 | 0 | 0 |
| 5: 1600/s | 0.46 | 1024 / 1024 | 18492 | 2077 | 111.4 | 0 | 0 |
| 6: 3200/s | 0.87 | 1024 / 1024 | 48843 | 2081 | 125.9 | 0 | 0 |

## Raw files

- `results.json`: every number above, plus the workload
- `requests.csv.gz`: one row per arrival (stage, kind, scheduled time, latency, status, dropped)
- `timeline.csv`: the server's gauges and counters, sampled every second
- `metrics-end.prom`: the server's full /metrics at the end of the run
