// Package projector maintains the PostgreSQL read model (orders, trades,
// ledger lines, balances) for history queries and reconciliation.
//
// # Design (week 6)
//
//   - Tails the durable journal and replays it through its own private copy
//     of state, regenerating deterministic events. It never depends on a
//     live, lossy in-process event stream.
//   - For each command: in ONE SQL transaction, insert event rows keyed by
//     (command_seq, event_index), update the projections, and advance
//     projection_checkpoint. If it crashes mid-way, the transaction rolls
//     back and the work is redone. Duplicate delivery is harmless.
//   - Exposes projected_seq so reconciliation compares state and SQL at the
//     *same* sequence.
//
// # Rules
//
//   - PostgreSQL is never consulted for risk checks or dedupe. It can be
//     dropped and rebuilt from the journal at any time.
package projector
