# 18 October 2026: matching (`book.Place`)

> **One-line summary:** the exchange can now trade. An incoming order
> walks the other side of the book from the best price, fills the oldest
> order first, pays each resting order's own price, and then rests or
> cancels whatever is left.

Plan reference: week 2. **Week 2 gate ✅:** "exact trades and book state
agree with the reference model; a saved command fixture replays
identically twice."

---

## 1. The six matching rules

| # | Rule | Example |
|---|---|---|
| 1 | **Price priority:** the best opposite price trades first | A buy meets the ask at 60,100 before 60,200 |
| 2 | **Time priority:** at one price, the oldest order (lowest `Seq`) trades first | #3 and #4 both bid 60,000; #3 came first, so it fills first |
| 3 | **Maker price:** every fill happens at the *resting* order's price | Buy limited at 60,200 fills 0.3 BTC at **60,100** (cheaper than its limit) |
| 4 | **Crossing:** trade while ask ≤ buy limit (or bid ≥ sell limit) | A buy at 60,100 stops when the next ask is 60,200 |
| 5 | **Self-trade prevention:** reaching your *own* resting order stops matching; the remainder is canceled; earlier fills stand | User 8 buying into user 8's ask: no trade |
| 6 | **Remainder:** GTC rests it at its limit price; IOC cancels it | IOC sell for 0.1 fills 0.05, cancels 0.05 |

**Maker** = the order already resting in the book (it "made" liquidity).
**Taker** = the incoming order (it "takes" liquidity).

### One order, step by step

```
Book before:            ASK 60200  0.2000  (#2)
                        ASK 60100  0.3000  (#1)
                        ----- spread -----
Incoming: BUY 0.4 BTC, limit 60200, GTC  (#5)

step 1  best ask 60100 ≤ 60200 → crosses.  Fill 0.3 @ 60100 vs #1.  #1 empty → removed.
step 2  best ask 60200 ≤ 60200 → crosses.  Fill 0.1 @ 60200 vs #2.  #2 keeps 0.1.
step 3  nothing left to buy → Outcome: filled.

Book after:             ASK 60200  0.1000  (#2, still first in its queue)
```

### The four outcomes

| Outcome | Meaning |
|---|---|
| `filled` | Everything traded |
| `rested` | GTC: the unfilled part now rests in the book |
| `canceled (ioc remainder)` | IOC: the unfilled part was canceled |
| `canceled (self-trade prevented)` | Hit the user's own order: stopped, rest canceled |

---

## 2. Why we built it this way

### Why "validate everything, then mutate" matters even more now

Matching changes many orders. If an error could happen halfway through
(after #1 was filled but before #2), the book would be half-changed and
money would be wrong. So `Place` runs **every** check first (`checkNew`).
After that point **nothing can fail**: the order is either rejected with
nothing touched, or fully applied. `TestPlaceRejects` includes orders that
*would* cross but are invalid, and proves the resting asks stay untouched.

### Why a partially filled maker keeps its place

A maker that's been partly filled is still the oldest order at its
price. Moving it to the back would punish it for trading. `maker.order.Qty`
just shrinks in place; the node never moves (`TestPartialFillKeepsQueuePosition`).

### Why self-trade prevention cancels the *incoming* order

The plan's policy (§07) is "cancel newest". The resting order was there
first and may be part of a strategy; the new order is the one that would
cause the wash trade. Fills already made against *other* users are real
trades and stand.

### Why a fully filled order still "uses up" its Seq

`Seq` is the global order of events. If order #2 (Seq 2) fills instantly
and disappears, Seq 2 still happened. Reusing it would make replay
ambiguous, so `TestFilledOrderStillConsumesItsSeq` checks it's rejected.

### Why no FOK (fill-or-kill) yet

FOK needs a dry run: "*could* this fill completely?", checked with exactly
the same liquidity and self-trade rules but without changing anything.
The plan defers it until the basics are proven.

---

## 3. How we changed the API safely: expand → migrate → contract

Week 1 had `Add` (reject crossing orders). Week 2 needed `Place` (match
them). Swapping in one go would leave commits that don't compile. So:

| Step | Commit(s) | State |
|---|---|---|
| **Expand** | `book.go` gains `checkNew` + `rest`; `match.go` adds `Place` | `Add` and `Place` both exist; everything green |
| **Migrate** | tests move from `Add` to `Place` one file at a time | Still green after every commit |
| **Contract** | `book.go` deletes `Add`; `errors.go` deletes `ErrWouldCross` | Only `Place` remains; still green |

Real teams use this pattern for database columns, APIs and config
renames. Every one of today's commits builds and passes on its own.

---

## 4. How we proved it works

| Evidence | What it shows |
|---|---|
| 🔴 **Red step** | Against a stub `Place`, **21 tests failed** |
| 🟢 **Rulebook table** | 15 scenarios in `TestPlaceMatching`: sweeps, FIFO, price limit, IOC, self-trade (4 cases), both sides |
| 🎲 **Reference matcher** | 5,000 random steps: the real book and a slow "scan everything" matcher must agree on **every trade, outcome, queue and depth** |
| 📼 **Golden fixture (the gate)** | `testdata/session.cmds` replayed twice into fresh books: identical output, and identical to `session.golden` |
| 🧪 **New invariant** | Rule 10: no zero-quantity "ghost" orders; a corruption test proves it's caught |
| ⏱ **Benchmarks** | Resting, crossing and cancel measured separately |

### The reference matcher, in numbers (fixed seed)

```
3,748 orders → 874 fills, 98 multi-level sweeps,
412 filled, 2,118 rested, 921 IOC remainders canceled,
297 self-trades prevented, 1,131 cancels, 121 unknown cancels
```

The test **fails** if any of those paths ever drops to zero, so a change to
the random mix can't silently stop testing something.

### Golden files

```
testdata/session.cmds    ← the input: a human-readable trading session
testdata/session.golden  ← the expected transcript, written BY HAND first
```

The golden file was worked out trade by trade on paper **before** the code
ran. The first run matched it line for line. That's stronger evidence than
generating the golden from the code's own output and trusting it.
`go test -run TestFixture -update` regenerates it after an intended
change; then you review the diff like code.

### Benchmarks (this laptop, Go 1.26.5)

| Operation | Time | Allocations |
|---|---|---|
| Place, resting | 206–432 ns | 1 (the queue node) |
| Place, crossing (one fill) | 191–206 ns | 1 (the `Fills` slice) |
| Cancel | ~108 ns | **0** |

Raw output with full context:
[evidence/bench/2026-10-18-book-microbench.txt](../../evidence/bench/2026-10-18-book-microbench.txt).
The resting number varied 2× between runs, which is laptop noise. That's
recorded, not hidden. These are in-memory costs only; the WAL's fsync
(week 4) will dominate real latency.

---

## 5. Go ideas met today

| Idea | Where | In one line |
|---|---|---|
| Built-in `min` | `match.go` | Go 1.21+: `min(a, b)` works for any ordered type |
| Enums with `String()` | `Outcome`, `TimeInForce` | Readable logs and test failures |
| Methods on result types | `Result.FilledQty()` | Derived values computed in one place |
| Expand → migrate → contract | today's commits | Change an API without ever breaking the build |
| Golden files + `testdata/` | `fixture_test.go` | Expected output in a reviewable file; `go` ignores `testdata` |
| Test flags | `-update` | `flag.Bool` in a test file is parsed by `go test` |
| Differential testing | `random_test.go` | Fast implementation vs a slow independent one |
| Benchmarks | `bench_test.go` | `testing.B`, `b.N`, `StopTimer`, `ReportAllocs`, `-benchmem` |
| Cheap permutation | `BenchmarkCancel` | `j*odd mod 2^k` visits every value once |

---

## 6. Try it yourself

```bash
go test -v -run TestPlaceMatching ./internal/book          # the rulebook
go test -v -run TestRandom ./internal/book                 # reference stats
go test -v -run TestFixture ./internal/book                # the gate
go test -run '^$' -bench . -benchmem ./internal/book       # benchmarks
go doc ./internal/book Book.Place
```

Edit `testdata/session.cmds` (add an order), run the fixture test, and
read the failure: it shows the first line that changed.

---

## 7. Check yourself

1. A buy limited at 60,300 meets asks at 60,100 and 60,200. What prices
   does it pay, and why?
2. A maker of 0.3 BTC is partly filled for 0.1. Where is it in the queue
   now?
3. User 8 sends a buy that would first hit user 3's ask, then user 8's own
   ask. What happens?
4. Why must `Place` do *all* validation before the first fill?
5. What is the golden file, and why was it written by hand?
6. Why does the random test fail if self-trades never happen?

---

## 8. Next session (week 3)

**The state machine: money.** One `State` owning the book *and* user
balances: test funding commands, **reservations** (a buy reserves USDT, a
sell reserves BTC), settlement of every fill with double-entry ledger
postings that sum to zero, and request deduplication. Then the engine's
single owner goroutine. That's where `Trade` and `Cancel`'s returned
remainder start moving real (simulated) money.
