# ADR-0002: Units and fixed-point arithmetic for BTC/USDT

- **Status:** Accepted
- **Date:** 2026-10-01

## Context

Floating point cannot represent most decimal amounts exactly
(`0.1 + 0.2 != 0.3`), and rounding differences break both ledger balancing
and replay determinism. We need exact integer amounts, and before writing
matching code we need a precise rule for converting price × quantity into
quote currency.

## Decision

Every amount is an `int64` count of the smallest unit. Named Go types
(`Price`, `Qty`, `Atoms`) stop us from mixing them by accident.

| Quantity | Unit | Example |
|---|---|---|
| Base lot (quantity) | 0.0001 BTC | 0.1 BTC = **1,000 lots** |
| Quote atom (money) | 0.000001 USDT | 1 USDT = **1,000,000 atoms** |
| Price tick | 1 USDT per BTC | 60,000 USDT/BTC = **60,000 ticks** |
| Notional | `priceTicks × qtyLots × 100` atoms | one tick × one lot = 0.0001 USDT = 100 atoms |

Worked example: 60,000 × 1,000 × 100 = 6,000,000,000 atoms = **6,000 USDT**.

Bounds (validated at the API, re-checked in the core):

- `1 ≤ price ≤ 1,000,000` ticks
- `1 ≤ quantity ≤ 100,000` lots (10 BTC)
- maximum order notional = 10⁶ × 10⁵ × 100 = 10¹³ atoms, far below
  `MaxInt64` ≈ 9.22 × 10¹⁸

Rules:

1. Parse decimal **strings** at the API. Reject negatives, zero, values with
   more decimal places than the unit allows (e.g. `"0.00001"` BTC), values
   out of range, exponents and empty strings. Parsing never goes through
   `float64`.
2. All multiplications are checked. For positive operands, multiplying
   `a × b` is rejected when `a > MaxInt64 / b`. Additions and balance
   credits are checked too. Overflow returns an error and never wraps.
3. Return money and IDs as JSON strings so JavaScript clients do not lose
   precision.
4. The symbol configuration (lot, tick, multiplier, bounds) carries a
   version and is part of recovery state.
5. Tier 1 has zero fees, so with these integral units fills never produce
   fractional atoms. Fees (Tier 2) round up: `ceil(N × bps / 10,000)`.

These are teaching parameters, not a claim about any real exchange's
specification.

## Consequences

- Easier: exact ledger sums, deterministic replay, simple tests.
- Harder: every boundary needs conversion code and tests, and adding an
  asset with different precision means new configuration.
- Evidence: table-driven tests for parsing and boundary arithmetic
  (week 1), plus fuzzing the parser so random strings never panic.
