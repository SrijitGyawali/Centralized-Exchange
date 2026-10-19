# 19 October 2026: the money side (`internal/state`)

> **One-line summary:** trades now move (simulated) money. Placing an
> order **reserves** funds, every fill **settles** through a
> **double-entry ledger** that always balances, leftovers are
> **released**, and a retried request **never runs twice**.

Plan reference: week 3 (first half: the state machine). Decisions recorded
in [ADR-0004](../../docs/adr/0004-accounting-reservations-ledger-idempotency.md).
Next session: the engine goroutine (concurrency), the second half of week 3.

---

## 1. The money flow of one order

```
                 ┌───────────┐  reservation   ┌───────────┐
  funding ──────►│ AVAILABLE │ ─────────────► │ RESERVED  │
  (issuance)     │  (free)   │ ◄───────────── │ (held for │
                 └───────────┘    release     │  orders)  │
                       ▲                      └─────┬─────┘
                       │        settlement          │
                       └──── counterparty ◄─────────┘
```

| Step | What happens | Example: Alice buys 0.1 BTC, limit 60,100 |
|---|---|---|
| **Reservation** | Hold what the order could possibly need | 0.1 × 60,100 = **6,010 USDT** held |
| **Settlement** | Each fill: buyer's USDT → seller, seller's BTC → buyer | Fills at Bob's 60,000: **6,000 USDT** ↔ **0.1 BTC** |
| **Release** | Return what's no longer needed | **10 USDT** back (price improvement) |

**Why reserve?** Without it, Alice could place two 6,000-USDT bids with
one 6,000-USDT balance. The second fill would then fail halfway through a
trade. Reserving makes double-spending impossible: the second order is
rejected up front for insufficient funds.

**What gets reserved:**
- **Buy:** USDT = limit price × quantity. It's the most it can ever pay,
  since it never pays above its limit.
- **Sell:** the BTC itself.

---

## 2. Double-entry bookkeeping, the ledger

Every change is a **journal** of **postings**. For each asset, postings
**sum to zero**: money moves, but is never created or destroyed. This is
the real output of `Example` in [example_test.go](../../internal/state/example_test.go):

```
journal 4.0 reservation
  USDT  user 1  available  -6010.000000
  USDT  user 1  reserved   +6010.000000      ← USDT sums to 0
journal 4.1 settlement
  USDT  user 1  reserved   -6000.000000
  USDT  user 2  available  +6000.000000      ← USDT sums to 0
  BTC   user 2  reserved   -0.1000
  BTC   user 1  available  +0.1000           ← BTC sums to 0
journal 4.2 release
  USDT  user 1  reserved   -10.000000
  USDT  user 1  available  +10.000000        ← USDT sums to 0
```

- **Where does money come from at all?** Test funding takes it from the
  system's **issuance** account, which goes *negative*. So even creating
  money balances: `issuance -10,000` / `alice +10,000`.
- **BTC and USDT never offset each other.** Each asset balances on its own.
- **Journal identity** `4.1` = (command Seq 4, journal #1 of that command).
  It's the same on every replay, which makes it a perfect SQL primary key
  later.
- **Balances are just the sum of all postings.** `tx.post` is the *only*
  code that touches balances, so the ledger always explains them.

---

## 3. "Can't fail halfway": the argument

A trade touches four balances. If anything could fail after the first
change, money would be half-moved. The design rules that out:

1. **Validate everything first:** fields, then available funds. A
   rejection changes nothing (no journal, no order ID used).
2. **Balances can't go negative** afterwards: funds were checked, and fills
   only consume what was reserved.
3. **Balances can't overflow:** total funding per asset is capped
   (`MaxSupply`: 21M BTC, 1B USDT). Balances are ≥ 0 and sum to the supply,
   so none can exceed it, and the supply is far below `MaxInt64`.

So once validation passes, every posting is guaranteed to succeed. If one
ever didn't, it would be a bug, and `tx.post` **panics** instead of letting
wrong money in.

---

## 4. Repeated requests: idempotency

**The story:** Alice places an order. It executes, but the response is lost
on the network. She can't tell whether it worked, so she retries with the
**same idempotency key**.

| Situation | What happens |
|---|---|
| Same key, **same** request | Returns the **stored** result, `Duplicate: true`. Nothing runs, no money moves. |
| Same key, **different** request | `RejectKeyConflict` (HTTP 409 later). The key keeps its first meaning. |
| Different users, same key | Independent: keys are scoped per user. |
| Retry of a **rejected** request | Still rejected, even if she's been funded since. A retry must never become a *different* trade. |
| Retry of a **cancel** | Duplicate. The reservation is released **exactly once**. |

**How "same request" is decided:** `prev.body == cmd.Body`. Go compares
interfaces by dynamic type *and* value, and our bodies are small comparable
structs, so this is exact, with no hashing and no collisions.

---

## 5. How we proved it

| Evidence | What it shows |
|---|---|
| 🔴 **Red step** | All 25 state tests failed against a stub `Apply` |
| 🧾 **Ledger replay, after every command** | Summing every journal ever produced gives *exactly* the current balances |
| ✅ **8 invariants, after every command** | Conservation, no negatives, supply cap, reserve function, reserved = sum of orders, state ↔ book |
| 🎲 **3,000 random commands** | Funding, orders, cancels, retries and conflicts from 4 users in a tight price band |
| 💰 **Independent wealth model** | Built only from funding + fills; must match `available + reserved` for every user, after every command |
| 🔁 **Determinism** | All 3,000 commands replayed into a fresh state give identical results, journals and final state, which is week 4's foundation |
| 🧪 **Corruption tests** | 7 ways to break money; `CheckInvariants` catches each |
| 💥 **Guard tests** | Unbalanced journal, negative balance, overflow: each makes `post` panic |
| 📄 **Worked example** | 22 lines of ledger output written by hand first; the code matched |

Random run (fixed seed): 887 fills, 218 price-improvement releases, 300
self-trades prevented, 252 exact retries, 114 key conflicts, 95
insufficient-funds rejections, 96 unknown-order cancels. The test fails if
any path drops to zero. Coverage: state 91.9%.

---

## 6. Go ideas met today

| Idea | Where | In one line |
|---|---|---|
| Sealed interface | `command.Body` | An unexported method means only this package can add body types |
| Type switch | `Apply` | `switch b := cmd.Body.(type)` picks the code per command kind |
| Interface `==` | dedupe | Compares dynamic type, then value; panics if the type isn't comparable |
| Slice aliasing, `Clone` | `Result.Clone` | Copying a struct copies the slice *header*, not the elements |
| `maps.Clone`, `maps.Equal`, `maps.DeleteFunc` | tests | Standard map helpers (Go 1.21+) |
| `reflect.DeepEqual` | replay test | Deep comparison of structs that hold slices |
| `panic` / `recover` / `defer` | `ledger_test.go` | Recover turns a panic back into a value a test can check |
| Struct keys in maps | `balanceKey` | A comparable struct is a perfect composite map key |
| Variadic functions | `tx.post(..., postings ...Posting)` | `move(...)...` spreads a slice into arguments |
| Errors vs results | `Apply` | Go `error` for caller bugs; `RejectReason` for business decisions |

---

## 7. Try it yourself

```bash
go test -v -run Example ./internal/state             # follow the money
go test -v -run TestRandom ./internal/state          # property-test stats
go test -v -run 'Retry|Conflict|Remembered' ./internal/state
go test -cover ./internal/state
go doc ./internal/state State.Apply
```

---

## 8. Check yourself

1. Alice bids 0.2 BTC at 60,000. How much is reserved, and in which asset?
2. She's filled for 0.1 at 59,900. Is that possible? (Hint: who's the maker?)
3. Why does a sell reserve BTC but a buy reserve USDT?
4. Write the four postings of "Bob sells Carol 0.5 BTC at 61,000".
5. Why is a rejected request remembered under its key?
6. Why can't `tx.post` ever fail on a validated order? Name the three reasons.
7. What does `-issuance` equal?

---

## 9. Next session (week 3, second half)

**The engine.** One goroutine that owns `State`: a bounded intake channel,
Seq assignment (the fairness point), 1-slot reply channels, copied
snapshots for readers, and race-detector tests where **many goroutines
submit at once**, including the same key concurrently, which must still
produce exactly one effect.
