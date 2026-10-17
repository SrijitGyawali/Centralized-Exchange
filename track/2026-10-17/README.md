# 17 October 2026: the order book (`internal/book`)

> **One-line summary:** the exchange can now hold orders. Bids and asks
> sit sorted by price, orders at the same price wait in a first-in,
> first-out queue, and any order can be cancelled instantly. No trading
> yet: matching comes in week 2.

Plan reference: week 1, session 3. **Week 1 gate ✅:** "add and cancel five
orders, print the expected book, and reject invalid units without panic"
is now a runnable test (`Example` in `example_test.go`).

---

## 1. What an order book looks like

```
          price    BTC    orders
ASK      60200   0.1500    1        ← sellers (asks): lowest price is best
ASK      60100   0.3000    1        ← best ask
----- spread 100 -----              ← gap between best buyer and best seller
BID      60000   0.3500    2        ← best bid: two orders queued here
BID      59900   0.5000    1        ← buyers (bids): highest price is best
```

That is the real output of our `Depth.String()`. The two orders at 60,000
are in a **queue**: whoever arrived first (lower `Seq`) trades first. That
is **price-time priority**.

---

## 2. The data structure

```
Book
├── bids  ladder  [60000] → [59900] → …     sorted slice, best first
│                    │
│                    └─ level 60000:  head ⇄ #1 ⇄ #2 ⇄ tail   (FIFO queue)
├── asks  ladder  [60100] → [60200] → …
└── orders  map[OrderID]*node  ── points straight at #1, #2, …
```

| Piece | Go type | Job |
|---|---|---|
| **ladder** | `[]*level`, sorted | Keeps prices in order; `levels[0]` is the best price |
| **level** | struct with `head`, `tail`, `orders`, `total` | All orders at one price, oldest first |
| **node** | struct with `prev`, `next`, `level` | One order inside a queue (doubly linked list) |
| **index** | `map[OrderID]*node` | Find any order in O(1), for cancel |

### What each operation costs

| Operation | Cost | Why |
|---|---|---|
| Add at an existing price | O(log P) + O(1) | Binary search for the level, append to the tail |
| Add at a new price | O(log P) + O(P) | `slices.Insert` shifts later levels |
| Cancel | O(1) | The index gives the node; unlinking is pointer work |
| Cancel the last order at a price | O(1) + O(P) | The empty level is deleted from the slice |
| Best bid / best ask | O(1) | It's always `levels[0]` |

(P = number of price levels on one side, usually small because prices
cluster near the spread.)

---

## 3. Why we built it this way

### Why a sorted slice and not a heap or tree?

| Structure | Best price | Ordered depth (top 10) | Delete any level |
|---|---|---|---|
| **Sorted slice** ✅ | O(1) | ✅ free: just read in order | O(P) |
| Heap | O(1) | ❌ heaps aren't fully sorted | awkward |
| Balanced tree (red-black / B-tree) | O(log P) | ✅ | O(log P) |

The slice is the simplest structure that's correct and easy to explain.
The plan says to **measure before replacing it** (page 7); a tree only wins
with thousands of price levels.

### Why a doubly linked list per price, not a slice?

Cancelling an order **in the middle** of a slice queue means shifting
everything after it. With a linked list, plus the index pointing at the
node, removal is just re-pointing two neighbours:

```
before:  #1 ⇄ #2 ⇄ #3        cancel #2:   #1.next = #3,  #3.prev = #1
after:   #1 ⇄ #3             nothing else moves → O(1)
```

We wrote our own list instead of `container/list` because that one stores
`any`, so every access needs a type assertion. Ours keeps exact types.

### Why reject orders that would cross?

A buy at or above the best ask should **trade**, not rest. Matching
doesn't exist until week 2, so for now `Add` refuses with `ErrWouldCross`.
This keeps the most important invariant true at all times:
**best bid < best ask** (the book is never "crossed").

### Why does the book check `Seq`?

FIFO is only correct if orders arrive in sequence order. Instead of
trusting every caller forever, `Add` rejects any `Seq` that isn't higher
than the last one (`ErrSeqNotIncreasing`). The rule is enforced in one
place.

### Why "validate everything, then mutate"?

`Add` runs **all** checks (fields, duplicate ID, sequence, crossing,
overflow) before touching anything. A rejected `Add` leaves the book
byte-for-byte unchanged; `TestAddRejects` proves it for 15 kinds of bad
order. The state package (week 3) will need exactly this property.

### Why is a second cancel an error?

Cancel #1 twice → the second gets `ErrUnknownOrder`. Later, each
successful cancel releases reserved money, so this is what makes a
duplicate cancel request **unable to release funds twice**.

### Why does everything return copies?

```go
d := b.Depth(0)      // fresh slices of plain values
d.Bids[0].Qty = 999  // changes the copy only, the book doesn't notice
```

If `Depth` returned `b.bids.levels[:n]`, the caller would hold a view
into the book's **live memory** (sub-slices share the backing array).
Later, other goroutines will read depth while the engine keeps changing
the book; copies make that safe. `TestSnapshotsAreCopies` scribbles on
every returned value and checks the book is untouched.

---

## 4. How we proved it works

| Evidence | What it shows |
|---|---|
| 🔴 **Red step** | Against a stub, **28 tests failed**, so the tests really check something |
| 🟢 **Unit tests** | Sorting, FIFO, cancel head/middle/tail, empty-level removal, 15 rejections, copies, depth limits |
| 🎲 **Reference model** | 5,000 random adds/cancels; a slow "obviously correct" map-based book must agree on **every** result, depth and queue (2,964 adds, 273 crossing rejects, 1,585 cancels, 178 unknown cancels) |
| 🔍 **Invariants after every step** | 9 structural rules checked after each of those 5,000 operations |
| 🧪 **Testing the tester** | 9 deliberate corruptions; `CheckInvariants` catches every one |
| 📄 **Example** | The week-1 gate, run and checked by `go test` |
| 📊 **Coverage** | book 94.7%, domain 97.2% |

### What is a reference model?

```
real Book:   sorted slices + linked lists + index   (fast, complex)
refBook:     one map, scan everything every time    (slow, obviously right)
```

Both get the same random operations. If they ever disagree, the fast one
has a bug. This is the plan's "reference-model comparison", and week 2's
matcher will get the same treatment.

### Why the random test picks from a slice, not a map

Go **randomizes map iteration order on purpose**. Picking "a random
order" by ranging over the map would differ on every run, so a failure
might never reproduce. With a fixed seed (`rand.NewPCG(2026, 10)`) and a
slice, every run is identical.

---

## 5. Go ideas met today

| Idea | Where | In one line |
|---|---|---|
| `iota` enums, invalid zero value | `domain.Side` | `Buy = iota + 1`, so a forgotten field isn't silently `Buy` |
| `fmt.Stringer` | `Side.String`, `Depth.String` | `fmt.Print(x)` calls `x.String()` |
| Pointers and linked lists | `level.go` | `prev`/`next` pointers; O(1) unlink |
| Maps as indexes | `Book.orders` | O(1) lookup by ID; `make` is required (writing to a nil map panics) |
| `slices.BinarySearchFunc`, `Insert`, `Delete` | `ladder.go` | Generic stdlib helpers (Go 1.21+) |
| `cmp.Compare` | `ladder.go`, tests | Returns -1/0/+1; reversing arguments flips the sort |
| Slice aliasing | `depth.go` | Sub-slices share memory; `make` a new one for a true copy |
| Two `%w` in one error | `order.go` | Matches both `ErrInvalidOrder` and `domain.ErrOutOfRange` |
| `panic` vs `error` | `ladder.removeLevel` | Errors for bad input; panic only for "this cannot happen" bugs |
| Black-box tests | `example_test.go` | `package book_test` sees only the exported API |
| White-box tests | `invariants_test.go` | Same package, so they can corrupt internals on purpose |
| `t.Helper()` | `mustAdd` | Failures point at the caller's line |
| `math/rand/v2` + fixed seed | `random_test.go` | Reproducible "random" tests |
| `maps.Values` + `slices.Collect` | `random_test.go` | Iterators (Go 1.23+) |

---

## 6. Try it yourself

```bash
go test -v -run Example ./internal/book            # the week-1 gate
go test -v -run TestRandom ./internal/book         # see the random-run stats
go test -cover ./internal/book
go doc ./internal/book                             # the public API
go doc ./internal/book Book.Add
```

---

## 7. Check yourself

1. Why does cancelling an order cost O(1), but cancelling the *last*
   order at a price cost O(P)?
2. What would go wrong if `Depth` returned `b.bids.levels[:n]` directly?
3. Why is `Side`'s zero value invalid instead of meaning `Buy`?
4. Why does `Add` reject a buy at 110 when the best ask is 110?
5. What is the reference model, and why can it be slow?
6. Why must the random test not pick orders by ranging over a map?

---

## 8. Next session (week 2)

**Matching.** An incoming buy at or above the best ask will now **trade**
instead of being rejected: walk the opposite side best-first, fill against
the oldest order at each price, execute at the **maker's** price, handle
partial fills, then rest the remainder (GTC) or cancel it (IOC). We'll also
build the slow reference matcher to check it against.
