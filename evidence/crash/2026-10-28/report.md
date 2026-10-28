# Drills: crash, retry, money, restore, recovery time

| | |
|---|---|
| started (UTC) | 2026-10-28T05:37:42Z |
| git commit | `bff76a457c70` |
| machine | windows/amd64, 8 logical CPUs, go1.26.10 |
| settings | 5 kill rounds, 32 clients over 16 users, seed 1, recovery sizes [100000,1000000] |

Every stop below is a hard kill (`Process.Kill`: SIGKILL on Unix, TerminateProcess on Windows) of the real `cexd` binary while 32 clients trade. This is the PROCESS-crash model: the operating system survives, so data written but not fsynced may survive too. Power loss and machine loss are not tested here (see docs/LIMITATIONS.md).

**Result: every check passed.**

## 1. Crash under load, then retry

Answered = the client got a definite result. Refused = 503 before admission (never ran). Unknown = no answer (killed in flight): retried with the same key.

| round | killed after | requests | answered | refused | unknown | recovered commands | torn tail cut | ready after restart | answers kept | refused never ran | unknown: already done | unknown: ran on retry | exactly once |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| 1 | 3014 ms | 34541 | 34498 | 0 | 43 | 34541 (in 139.9404ms) | 0 B | 159 ms | 34498 / 34498 | 0 / 0 | 11 | 32 | 43 / 43 |
| 2 | 2004 ms | 22515 | 22483 | 0 | 32 | 57123 (in 279.5113ms) | 0 B | 298 ms | 22483 / 22483 | 0 / 0 | 13 | 19 | 32 / 32 |
| 3 | 3400 ms | 36052 | 36011 | 0 | 41 | 93215 (in 490.6288ms) | 0 B | 514 ms | 36011 / 36011 | 0 / 0 | 17 | 24 | 41 / 41 |
| 4 | 3426 ms | 26952 | 26888 | 0 | 64 | 120203 (in 713.8531ms) | 0 B | 735 ms | 26888 / 26888 | 0 / 0 | 18 | 46 | 64 / 64 |
| 5 | 1518 ms | 9236 | 9183 | 0 | 53 | 129539 (in 905.9455ms) | 0 B | 932 ms | 9183 / 9183 | 0 / 0 | 25 | 28 | 53 / 53 |

"Unknown: already done" are LOST RESPONSES: the command was journaled and executed, the client never heard back, and the retry returned the original result instead of running it twice. A torn tail is rare here: a kill must land inside one write system call, against ~1 ms spent in fsync; torn tails and corrupt frames are covered deterministically by tests/crash and the journal tests.

Money checks after every kill (offline replay of the journal, before the restart):

| round | last seq | invariants | issued = held (USDT, BTC) | digest = what the restart recovered |
|---|---|---|---|---|
| 1 | 34541 | ok | true | true (`843604484c20512a…`) |
| 2 | 57123 | ok | true | true (`e06d565bf67c74f4…`) |
| 3 | 93215 | ok | true | true (`e4ec75993786e333…`) |
| 4 | 120203 | ok | true | true (`83bac9556397b54f…`) |
| 5 | 129539 | ok | true | true (`742a64571c31e9d0…`) |

## 2. Restore from a copied journal

A hot copy is taken while trading continues (what a naive file backup of a running server gets); a cold copy after the kill. Each is restored on a fresh directory by starting `cexd` on it.

| copy | copied after | bytes | restored to seq | original last seq | torn tail cut | exact prefix of the original | digest = original at that seq | answers kept | answers after the copy point (lost) | wrongly lost |
|---|---|---|---|---|---|---|---|---|---|---|
| hot | 2018 ms of 3534 | 841974 | 16393 | 30742 | 0 B | true | true | 16361 | 14323 | 0 |
| cold | 3534 ms of 3534 | 1578788 | 30742 | 30742 | 0 B | true | true | 30684 | 0 | 0 |

A restore loses exactly what happened after the copy was taken (the recovery point), and nothing before it.

## 3. Recovery time

`node.Open` on a journal written through the real engine (group commit, mixed orders and cancels from 16 users): scan, both CRCs, `Apply`, every invariant, the digest. Three runs each.

| commands | journal | written in | recovery (3 runs) | median | replay rate | heap after recovery |
|---|---|---|---|---|---|---|
| 100000 | 4.2 MiB | 3.5 s | 1.167s, 1.239s, 1.203s | **1.203s** | 83136 commands/s | 37 MiB |
| 1000000 | 42.5 MiB | 33.6 s | 8.006s, 8.631s, 7.92s | **8.006s** | 124903 commands/s | 432 MiB |

## Raw files

- `results.json`: every number above
- `crash-server.log`, `restore-*-server.log`: the servers' own JSON logs, including each `recovered` line

Reproduce: `go run ./tests/drills -out <dir>` (about 5 minutes; `-crash-rounds 1 -sizes ""` for a quick run).
