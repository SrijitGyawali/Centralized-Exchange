// Package marketdata publishes public market data (order-book depth and
// trades) to WebSocket subscribers.
//
// # Contract (week 6, Tier 1)
//
//   - Bounded-frequency full depth snapshots, each tagged with the applied
//     sequence it reflects.
//   - Explicit fan-out: each subscriber has its own bounded buffer and
//     writer goroutine. (A single Go channel with many receivers
//     *distributes* messages and does not broadcast them.)
//   - Slow consumers are disconnected and must resubscribe. They are never
//     allowed to block the engine, and depth changes are never silently
//     skipped.
//
// Tier 2 adds per-symbol incremental updates with resync: subscribe, buffer,
// fetch snapshot S, discard updates <= S, then require a contiguous suffix.
package marketdata
