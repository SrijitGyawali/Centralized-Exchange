// Package marketdata publishes public market data, the order book and the
// recent trade tape, to any number of WebSocket viewers (GET /stream/book),
// without ever slowing the matching engine down (ADR-0010).
//
//	engine.Snapshot()  (atomic, lock-free, never the engine goroutine)
//	      │  every Interval, only if Seq changed
//	      ▼
//	Hub ── encode ONCE ──► subscriber 1: bounded chan ──► its own writer goroutine ──► browser
//	                    ├─► subscriber 2: …
//	                    └─► subscriber N: chan full? → disconnected ("slow consumer")
//
// # Contract (Tier 1)
//
//   - Every message is a COMPLETE snapshot (full depth + last 50 trades)
//     tagged with the Seq it reflects. One message is enough to draw the
//     screen, and reconnecting is the only recovery a client needs.
//   - Bounded rate: at most one message per Interval (100 ms), and only when
//     something changed.
//   - Explicit fan-out: one bounded channel per subscriber. A single shared
//     Go channel would DISTRIBUTE messages, not broadcast them.
//   - Slow consumers are disconnected with a reason. They can never block the
//     hub, other subscribers, or the engine.
//   - Public data only: fills carry price, quantity, side and Seq, never
//     users or order IDs.
//
// Tier 2 adds incremental updates with per-symbol sequence numbers and gap
// detection (subscribe, buffer, fetch a snapshot, apply a contiguous
// suffix), plus authenticated private order updates.
package marketdata
