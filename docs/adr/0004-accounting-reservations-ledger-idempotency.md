# ADR-0004: Accounting: reservations, double-entry ledger and idempotency

- **Status:** Accepted
- **Date:** 2026-10-19

## Context

Matching (week 2) decides *who trades with whom*. It does not move money.
The state machine must now guarantee four things, under any sequence of
commands, retries and crashes:

1. A user can never spend the same funds twice (two orders, one balance).
2. Every unit of money is explainable: where it came from, where it went.
3. A trade can never be half-settled.
4. A retried request (after a lost response) never executes twice.

Tier 1 has zero fees and one symbol, BTC/USDT (ADR-0001, ADR-0002).

## Options considered

1. **Check funds at fill time only.** Simple, but two resting orders could
   both rely on the same balance, and the second fill would fail
   mid-match. ❌
2. **Reserve funds when an order is placed** ✅. The order holds what it
   could possibly need. Fills consume the reservation, and leftovers are
   released.
3. **Store balances only** vs **double-entry journals** ✅. With balances
   only, a wrong balance can't be explained. With journals, every change
   is a balanced record, and balances are their sum.
4. **Idempotency by request hash** vs **by the full request** ✅. A hash
   saves memory; comparing the stored request itself with `==` is exact,
   with no collisions. Bodies are tiny, comparable structs.

## Decision

### Units and accounts
- Balances are `int64` in each asset's smallest unit (BTC lots,
  USDT atoms), the same units the book uses. No conversion step.
- Each user has **available** and **reserved** buckets per asset. The
  system user (ID 0) has an **issuance** account per asset, which is the
  counterpart of test funding and goes negative.

### Reservation (the reserve function)
- **Buy:** `Notional(limit price, remaining qty)` USDT. That's the most it
  could pay, because it never pays more than its limit.
- **Sell:** `remaining qty` BTC.
- Each open order's reservation must equal this function at all times
  (`CheckInvariants`, rule 6).

### Settlement and release
- Fills execute at the **maker's** price (book rule 3), so:
  - a resting buy pays exactly its limit, and its reservation shrinks
    exactly;
  - a buying taker may pay less than its limit. The surplus
    (**price improvement**) is released once, after matching, as
    `reserved − reserve function(remainder)`.
- IOC remainders, self-trade-prevented remainders and cancels release
  everything still reserved.

### Ledger
- Every change is a `Journal` of signed `Postings` that **sum to zero per
  asset** (BTC and USDT never offset each other).
- Journal kinds: funding, reservation, settlement, release.
- Identity: `(command Seq, index within the command)`, which is stable
  across replays and will be the SQL primary key in week 6.
- `tx.post` is the only code that changes balances. It refuses unbalanced
  journals, negative user balances and overflow by panicking, because these
  can only be bugs.

### Can't fail halfway
- All validation (fields, funds) runs **before** the first change.
- **Supply cap:** total funding per asset ≤ `MaxSupply` (21M BTC, 1B USDT).
  Since balances are non-negative and sum to zero, no balance can exceed
  the supply, so no credit can overflow.
- Together these mean: once an order passes validation, settlement cannot
  fail. A command is either rejected untouched or applied completely.

### Idempotency
- Key scope: **(user, key)**. Keys are 1–64 bytes, bounding memory.
- Same key + same request → the **stored result**, marked `Duplicate`.
  Nothing executes and no journals are produced.
- Same key + different request → `RejectKeyConflict` (HTTP 409). The key
  keeps its original meaning.
- **Rejections are remembered too.** A retry of an insufficient-funds
  request stays rejected even after funding; a new attempt needs a new
  key. A "retry" must never silently become a different trade from the
  one the client was told about.
- The stored result answers "what happened to my request?", not "what is
  the order's status now?". Status queries are a separate API.
- Stored results are cloned on the way in and out (they hold a slice).

### Privacy
- Cancelling an order that doesn't exist, is finished, or belongs to
  someone else gets the same `RejectUnknownOrder`, so order IDs can't be
  probed.

## Consequences

- **Easier:** every balance is explainable from journals; conservation,
  reservations and wealth are all checkable after every command; retries
  are safe; the week-6 SQL projection can key on journal identity.
- **Harder:** the dedupe table grows with every request. Tier 1 accepts
  that, and later tiers need a documented retention window with *replayed*
  expiry commands (plan §24). A key's result is fixed forever, which is
  the correct but sometimes surprising behaviour for clients.
- **Not yet:** fees (Tier 2, with a proven reserve bound for per-fill
  rounding), multiple symbols, withdrawals.
- **Evidence:** unit tests for every flow, including the plan's worked
  example; a 3,000-command random property test with an independent
  wealth model; ledger replay equals balances after every command; full
  replay into a fresh state reproduces identical effects; corruption tests
  for every invariant; panic tests for every guard.
