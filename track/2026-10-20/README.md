# 20 October 2026: the engine (`internal/engine`)

> **One-line summary:** many goroutines can now trade at once. One
> **owner goroutine** takes their requests from a queue, numbers them,
> applies them one at a time, and hands each caller its result. No locks
> in the trading code, and the race detector agrees.

Plan reference: week 3, second half. **Week 3 gate ✅:** "every generated
command preserves account and order invariants; same-key concurrency
creates one effect; race tests exercise submissions and reads."
Decisions recorded in [ADR-0005](../../docs/adr/0005-acknowledgement-and-request-lifecycle.md).

---

## 1. The picture

```
 many goroutines                    ONE owner goroutine (Run)
 ───────────────                    ─────────────────────────
 Submit(ctx, …) ─┐                  for req := range intake {
 Submit(ctx, …) ─┼─► bounded ───►       Seq++               ← who was "first"
 Query(ctx, …)  ─┘   queue              state.Apply(cmd)    ← the money logic
                     (channel)          publish Snapshot    ← for readers
                                        reply (1-slot chan) ← to the caller
 Snapshot() ◄──── lock-free         }
```

**Why one goroutine instead of a mutex?** With a mutex, whoever grabs the
lock first goes first, and that order is never recorded. With one owner,
every command gets a **Seq**, and that number *is* the order. Week 4
writes it to disk, and replay follows it exactly.

---

## 2. A request's life: "did my order happen?"

This is the most important idea today. The answer depends on **when** an
error happens:

| Moment | Errors | Did it run? |
|---|---|---|
| **Before admission** (never entered the queue) | invalid, overloaded, closed, halted, context already done | ❌ **No, definitely** |
| **After admission, no reply** (caller timed out, or the engine halted) | `ErrOutcomeUnknown` | ❓ **Maybe**. Retry with the same key to find out. |
| **Reply received** | `nil` + `Result` | ✅ **Yes**, and the result says how (accepted or rejected) |

**Timeouts are not rollback.** Once admitted, a command *will* run, even if
its caller gives up. `TestTimeoutAfterAdmissionIsUnknownButTheCommandStillRuns`
proves it: the caller times out, the command runs later, and a retry with
the same key returns `Duplicate: true, Seq: 1`.

---

## 3. The Go concurrency tools, and why each one is there

| Tool | Where | Why |
|---|---|---|
| **Buffered channel (queue)** | `intake` | Hands requests to the owner, in order, with a size limit |
| **Non-blocking send** (`select` + `default`) | `enqueue` | A full queue answers "overloaded" *now* instead of making callers wait |
| **1-slot reply channel** | `Submit` | The owner sends exactly once and never blocks, even if the caller left. No goroutine leak. |
| **`select` over 3 channels** | `Submit` | Wait for whichever comes first: the result, the caller's context, or a halt |
| **`sync.RWMutex`** | `enqueue` / `Close` | Sending on a closed channel **panics**. Senders hold the read lock; `Close` needs the write lock. |
| **`atomic.Pointer[Snapshot]`** | `publish` / `Snapshot` | Readers grab the latest snapshot with no lock; it's immutable, so sharing it is safe |
| **`atomic.Bool`** | `Run` | `Run` twice would mean two owners, so it panics instead |
| **Closing a channel to broadcast** | `halted`, `done`, test `start` | A closed channel wakes **every** receiver at once |
| **`defer` + `recover`** | `handle` | A panic in `Apply` halts the engine instead of crashing the process |
| **Happens-before** | `haltErr` / `halted` | Write the error *then* close the channel, so anyone who sees it closed sees the error |
| **Generic function** | `Query[T]` | Go methods can't have type parameters, so `Query` is a function |
| **Consumer-defined interface** | `StateReader` | Leaves out `Apply`, so queries are read-only *by type* |
| **`var _ I = (*T)(nil)`** | `query.go` | A compile-time check that `*state.State` implements `StateReader` |

### The two `select` subtleties worth remembering

1. **`select` picks randomly among ready cases.** The result and a
   cancellation can arrive together, so `lastChance` checks once more for
   the result: a known outcome beats "unknown".
2. **A nil or closed channel changes `select`.** A closed channel is
   *always* ready, which is exactly why closing `halted` wakes every
   waiting `Submit` at once.

---

## 4. Two ways to read

| | `Snapshot()` | `Query()` / `Balance()` |
|---|---|---|
| For | Public market data (order book) | Anything, including private balances |
| Cost to the engine | None (lock-free) | Runs on the owner, in the queue |
| Freshness | Published after every command, **before** the reply | Sees every command whose reply came back before it was sent |
| Safety | Immutable value behind an atomic pointer | Read-only `StateReader` interface |

---

## 5. How we proved it

| Test | What it shows |
|---|---|
| 🔴 **Red step** | 13 of 14 engine tests failed against stubs |
| 👥 **100 goroutines, same key, same moment** | Exactly **one** executes; 99 get its result as duplicates; one reservation, one order |
| ⚔️ **50 goroutines, one key, 50 different orders** | One winner, 49 key conflicts, only the winner's order in the book |
| 🏭 **8 traders + 4 readers concurrently** | 2,000 commands with gap-free Seqs; readers never see time go backwards; invariants hold; money conserved |
| ⏱ **Timeout after admission** | `ErrOutcomeUnknown`, yet the command runs; retry reveals it |
| 🚦 **Overload** | A full queue refuses immediately; the refused request never runs |
| 🛑 **Close** | Refuses new requests, drains admitted ones, `Done` fires |
| 💥 **Halt** on an `Apply` error and on a panic | Trading stops; `Err()` explains; nothing more admitted |
| 🧪 **Testing the race detector** | A deliberately buggy test that read state from the wrong goroutine **failed** with `WARNING: DATA RACE`, then was deleted |
| 🧊 **Why capacity 1** | With an unbuffered reply channel, one abandoned caller froze the engine (test timed out after 15 s), then reverted |

### Benchmarks (this laptop)

| | Per command | Allocations |
|---|---|---|
| `state.Apply` directly | ~1.0 µs | 6 |
| `Submit`, one caller | ~2.7 µs | 9 |
| `Submit`, 8 callers at once | ~2.0 µs | 9 |

The handoff costs about 1.7 µs, mostly waking goroutines. With many
callers the queue is rarely empty, the owner rarely sleeps, and it gets
*faster*. It's still one writer: less waking, not parallelism. Raw output:
[evidence/bench/2026-10-20-engine-overhead.txt](../../evidence/bench/2026-10-20-engine-overhead.txt).
From week 4, the WAL fsync will dwarf all of this.

---

## 6. Go ideas met today (new ones)

| Idea | In one line |
|---|---|
| `sync.WaitGroup.Go` | Go 1.25+: `wg.Go(func(){…})` replaces `Add(1)` + `go` + `defer Done()` |
| `t.Context()` | Go 1.24+: a context canceled automatically when the test ends |
| `b.Context()`, `b.RunParallel` | Benchmark helpers for contexts and many goroutines |
| `t.Error` vs `t.Fatal` in goroutines | `Fatal` must only be called from the test's own goroutine |
| `slices.Sorted(iterator)` | Collect and sort values from an iterator (Go 1.23+) |
| Barrier via `close(start)` | Release many goroutines at the same instant |
| `-race` | Instruments memory accesses; catches races only on paths that run |

---

## 7. Try it yourself

```bash
go test -race -v ./internal/engine
go test -race -run SameKey -count=20 ./internal/engine     # hammer it
go test -run '^$' -bench . -benchmem -benchtime=200000x ./internal/engine
go doc ./internal/engine Engine.Submit
```

**Experiment (verified today):** in `submit.go`, change
`make(chan command.Result, 1)` to `make(chan command.Result)` (unbuffered)
and run:

```bash
go test -run TestTimeoutAfterAdmission -timeout 15s ./internal/engine
```

Result: `panic: test timed out after 15s`. The caller gave up, so nobody
receives the reply, and the engine's send blocks **forever**. Trading
freezes for everyone because of one impatient client. Capacity 1 is the
whole fix. Put it back afterwards.

---

## 8. Check yourself

1. A client's request times out. Did the order happen? What should it do?
2. Why does the reply channel have capacity 1, and what breaks with 0?
3. Why can't `Close` just call `close(e.intake)` without the mutex?
4. Why is the snapshot published *before* the reply is sent?
5. Two requests arrive "at the same time". Who goes first, and where is
   that recorded?
6. Why does `Query` take a `StateReader` and not a `*state.State`?
7. What does the race detector *not* prove?

---

## 9. Next session (week 4)

**Durability.** The write-ahead log: frame format, append + fsync
*before* Apply, crash recovery by replay, a corrupt-vs-torn-tail policy,
and a crash harness that kills the process at injected points and proves
every acknowledged command survives exactly once.
