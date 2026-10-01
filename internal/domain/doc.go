// Package domain defines the vocabulary of the exchange: units of value,
// order enums, identifiers and checked arithmetic.
//
// It is the innermost package of the pure core and imports only the standard
// library. Everything else in the system depends on it.
//
// # Responsibilities (week 1)
//
//   - Named integer types: Price (ticks), Qty (lots), Atoms (quote units).
//     Named types make the compiler reject Price + Qty by accident.
//   - Exact decimal-string parsing ("60000", "0.1") into those units without
//     ever converting through float64. See docs/adr/0002-units-and-fixed-point.md.
//   - Checked arithmetic: multiplication and addition return (value, error)
//     and never wrap on overflow.
//   - Enums: Side (Buy/Sell), TimeInForce (GTC/IOC), OrderStatus.
//   - Identifiers: UserID, OrderID, Seq (global command sequence).
//   - Symbol configuration (lot, tick, multiplier, bounds, version).
//
// # Rules
//
//   - No I/O, no time.Now, no randomness, no global mutable state.
//   - Constructors validate. A value of type Price that exists is in range.
package domain
