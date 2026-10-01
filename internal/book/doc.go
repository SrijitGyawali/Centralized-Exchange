// Package book implements a single symbol's limit order book and its
// price-time priority matching.
//
// # Data structure (weeks 1-2)
//
//   - Bids sorted high→low and asks sorted low→high, as slices of price
//     levels. Inserting a new level costs O(P), where P is the number of
//     levels.
//   - Each level is a FIFO doubly linked list of resting orders, ordered by
//     admission sequence.
//   - An order-ID index points to list nodes, so cancel unlinks in O(1).
//
// # Matching contract (Tier 1)
//
//   - Best price first; FIFO by sequence at equal price.
//   - Trades execute at the resting (maker) order's price.
//   - GTC: match while prices cross, then rest the remainder.
//   - IOC: match while prices cross, then cancel the remainder.
//   - Self-trade prevention: on reaching a maker from the same user, cancel
//     the incoming remainder. Earlier fills stand.
//
// # Rules
//
//   - Pure and single-threaded. The book is owned by state, which is owned
//     by the engine goroutine. It has no mutex because only one goroutine
//     ever touches it.
//   - Depth snapshots returned to callers are deep copies, never slices that
//     alias internal storage.
package book
