# CPU profile at 3200 orders/s: the engine waits on fsync

Taken by `cmd/loadgen -profile-stage 6` (18 s inside the 3200 orders/s stage
of `loadtest/workloads/step.json`, `CEX_BATCH_SIZE=64`), from cexd's private
admin port (`/debug/pprof/profile`).

| Profile | Commit | Raw file |
|---|---|---|
| before | `b3c2d12` | [`../load/2026-10-27-step-batch64/cpu.pprof`](../load/2026-10-27-step-batch64/cpu.pprof) |
| after the one optimization | `a384d9a` | [`../load/2026-10-27-step-batch64-after-observer/cpu.pprof`](../load/2026-10-27-step-batch64-after-observer/cpu.pprof) |

```bash
go tool pprof -top -cum evidence/load/2026-10-27-step-batch64/cpu.pprof
go tool pprof -top -cum -nodefraction=0 evidence/load/2026-10-27-step-batch64/cpu.pprof | grep -E 'publish|Applied|GetMetricWithLabelValues'
```

## What it shows (before, 28.87 s of samples in 18 s)

| Where | Cumulative | Share |
|---|---|---|
| `engine.(*Engine).handleBatch` (the owner goroutine) | 16.71 s | 57.9 % |
| … of which `syscall.FlushFileBuffers` (fsync) | 15.54 s | 53.8 % |
| `net/http.(*conn).serve` (all HTTP work, including socket I/O) | 7.52 s | 26.1 % |
| `runtime.gcBgMarkWorker` (GC) | 0.72 s | 2.5 % |
| `encoding/json.(*Decoder).Token` (strict request decoding) | 0.61 s | 2.1 % |
| `state.(*State).Apply` (the matching engine proper) | 0.37 s | 1.3 % |
| `engine.(*Engine).publish` (snapshot per command) | 0.14 s | 0.5 % |
| `telemetry.engineObserver.Applied` | 0.05 s | 0.2 % |

**Read with care on Windows:** Go's CPU profiler there samples threads that
are blocked inside a system call as if they were running. "53.8 % in
FlushFileBuffers" is therefore WAITING for the disk, not CPU work. The
process really used about 1.06 CPU cores in this stage (from
`process_cpu_seconds_total`).

## Interpretation

- The single owner goroutine spends about **93 %** of its time
  (15.54 / 16.71 s) waiting for fsync. Its CPU work (apply, publish,
  observer) is under 1 s of 18 s. Making the engine's CPU path faster cannot
  noticeably change latency or throughput on this disk.
- Group commit is what keeps up: the mean batch grows with load
  (1 → 2 → 5 commands per fsync from 400 to 3200 orders/s), so throughput
  follows demand without a timer.
- The tail comes from the device: rare fsyncs of 100 ms - 1 s (see
  [`labs/fsync-tail/NOTES.md`](../../labs/fsync-tail/NOTES.md)).

## The one optimization, and what it changed

Among the engine goroutine's small CPU costs, the per-command observer did
two `GetMetricWithLabelValues` lookups (hash + read lock). Commit `2ff20fd`
resolves those children once:

- Microbenchmark: 221.9 ns → 61.0 ns per call (−72.5 %, p=0.000, n=8;
  [`../bench/2026-10-27-clock-and-observer.txt`](../bench/2026-10-27-clock-and-observer.txt)).
- In the profile: `Applied` 0.05 s → 0.01 s of 18 s.
- End to end: **not measurable.** The before/after step runs differ far more
  from where the random fsync stalls happened to land than from 160 ns per
  command (see [docs/PERFORMANCE.md](../../docs/PERFORMANCE.md)). Kept
  because it is simple, safe and removes a lock from the serial path, not
  because it made the system faster.

The `GetMetricWithLabelValues` still visible after the change (0.06 s)
comes from the HTTP middleware and refusal counting, on request goroutines,
not the owner.
