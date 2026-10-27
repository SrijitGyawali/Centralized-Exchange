# Lab: where do 100 ms – 1 s fsyncs come from?

Week 7. Machine: Windows 11, i7-1165G7, KIOXIA 512 GB consumer NVMe SSD,
NTFS. Timings use `internal/clock` (QueryPerformanceCounter), because
`time.Now` on this machine only advances every 578 µs.

## Why this lab exists

The load tests showed rare fsyncs of 100 ms to 1 s inside `cexd`
(`cex_journal_fsync_seconds`: 16 of 49,824 above 100 ms in one step run).
The engine is a single writer, so one slow fsync stalls every request queued
behind it. At 800 orders/s that produced a client p95 of 612 ms while the
median fsync was 1.3 ms. The same tail broke the "p99 < 50 ms at 100/s"
hypothesis in the 10-minute steady run (p99 248 ms).

## Prediction (hypothesis 1)

The journal grows with every append. On NTFS, extending a file changes its
metadata (size, allocation), so each `FlushFileBuffers` must also flush the
file system's metadata and log. **Prediction: writing into space that is
already allocated, and already written, removes the long fsyncs.** If true,
the fix is to preallocate the journal in chunks (which would change the
recovery format: zeros after the last frame read as "bad magic" today).

## Command

```bash
go run ./labs/fsync-tail -n 10000 -rounds 2                    # three modes, interleaved
go run ./labs/fsync-tail -modes grow -rounds 3 -n 5000         # used for hypothesis 2
```

Modes, one 128-byte write + one fsync per record:
`grow` (append to a new file, like the journal), `truncate` (size set first,
no data written), `zerofill` (every byte written and synced first, then
overwritten).

## Observation 1: preallocation changes nothing

| mode | round | p50 ms | p90 | p99 | p99.9 | max | > 10 ms | > 100 ms |
|---|---|---|---|---|---|---|---|---|
| grow | 1 | 1.220 | 1.486 | 2.526 | 8.598 | 30.166 | 8 | 0 |
| truncate | 1 | 1.274 | 1.520 | 2.545 | 10.627 | 33.836 | 11 | 0 |
| zerofill | 1 | 1.293 | 1.529 | 2.330 | 9.305 | 23.474 | 8 | 0 |
| grow | 2 | 1.301 | 1.539 | 2.294 | 9.614 | 30.302 | 8 | 0 |
| truncate | 2 | 1.313 | 1.535 | 2.194 | 6.582 | 65.502 | 2 | 0 |
| zerofill | 2 | 1.257 | 1.535 | 2.409 | 7.744 | 28.936 | 4 | 0 |

**Hypothesis 1 rejected.** The three distributions are the same within
noise. File growth is not what makes fsync slow here, so preallocation (and
the recovery-format change it would need) is not worth doing.

## Hypothesis 2: is it the load?

None of those 60,000 fsyncs exceeded 100 ms, yet the server saw several
per minute under load. Second prediction: **the long fsyncs need concurrent
load.** Test: the same lab while a fresh `cexd` takes 400 orders/s, then
again with the machine idle.

| when | round | p50 ms | p90 | p99 | p99.9 | max | > 10 ms | > 100 ms |
|---|---|---|---|---|---|---|---|---|
| during 400 orders/s | 1 | 2.190 | 2.543 | 3.500 | 10.576 | 30.554 | 8 | 0 |
| during 400 orders/s | 2 | 2.184 | 2.550 | 3.907 | 12.044 | 16.349 | 10 | 0 |
| during 400 orders/s | 3 | 2.203 | 2.768 | 8.098 | 42.420 | 93.309 | 25 | 0 |
| idle | 1 | 1.233 | 1.527 | 2.916 | 21.860 | 88.340 | 12 | 0 |
| idle | 2 | 1.254 | 1.552 | 2.841 | 6.487 | 11.378 | 2 | 0 |
| idle | 3 | 1.225 | 1.549 | **43.466** | 86.313 | **177.239** | 60 | **3** |

(The server itself, during those 70 s: 27,318 fsyncs, 205 above 10 ms,
none above 100 ms.)

## Explanation

1. **Concurrent fsyncs slow each other down.** With the server fsyncing its
   own journal, the lab's median nearly doubled (1.2 → 2.2 ms) although the
   two processes write different files. On a consumer NVMe drive without
   power-loss protection, an fsync makes the DRIVE flush its volatile write
   cache. The cache is shared, so every writer pays for everyone's data.
2. **The long tail is the device and the OS, and it comes in bursts.** The
   worst burst of the whole lab happened with the machine IDLE (round 3:
   60 fsyncs over 10 ms, 3 over 100 ms, max 177 ms). Plausible causes, not
   proven here: the SSD's internal garbage collection or SLC-cache flushes,
   other processes' writes (Docker Desktop's VM disk lives on the same
   drive), Windows background I/O. What the lab proves is the negative:
   neither our file layout nor our process is required to produce it.
3. Hypothesis 2 is therefore only half right: load makes fsync slower on
   average (contention), but the 100 ms+ outliers do not need it.

## The design choice it informed

- **No preallocation.** It would complicate recovery (a zero-filled tail
  would have to be told apart from corruption) for no measured gain.
- **Keep group commit, and value it for stall recovery.** After a stall,
  backlogged commands share fsyncs (up to 64 per fsync), so the backlog
  clears in a few milliseconds. With one fsync per order, the same stall
  compounds: in the step test, batch=1 had a p95 of 1.75 s at only 200
  orders/s, far below its nominal capacity (~730/s).
- **The queue size is a policy, not a fix.** It decides how long a stall
  can be absorbed (1024 slots = 320 ms at 3200/s) before requests are
  refused with 503 instead of waiting.
- **For a real deployment, the storage matters more than the code:** drives
  with power-loss protection can acknowledge a flush from protected cache,
  which makes fsync both fast and predictable. This is a hardware
  requirement to write down, not something a Go change can buy.
