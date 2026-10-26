// Package projector maintains the PostgreSQL read model: commands, orders,
// trades, the double-entry ledger and running balances, for history queries
// and reconciliation (ADR-0009).
//
//	journal.wal ──ScanFrom(offset, next seq)──► private state.Apply ──► plan() ──► one SQL transaction
//	                                                                              rows + checkpoint, together
//
// # Files
//
//	migrations/  the schema, embedded into the binary and applied with tern
//	migrate.go   Migrate: runs pending migrations on every start
//	plan.go      plan: one applied command -> its SQL statements (pure, unit-tested)
//	projector.go New (rebuild to the checkpoint), CatchUp, write, Reconcile
//	run.go       Run: the supervisor cexd starts; Status for /v1/status
//
// # Design rules
//
//   - The journal is the source. The projector replays it through its OWN
//     state.State, never a live event stream that could drop messages.
//   - Each batch of rows and the new checkpoint commit in ONE transaction,
//     with the checkpoint row locked and checked first. A crash at any moment
//     leaves the database exactly at a committed checkpoint; restarting
//     rebuilds the replay state to it and continues. Nothing is skipped or
//     doubled, and primary keys on journal identity back that up.
//   - It only reads complete frames; an incomplete last frame (the engine is
//     mid-append) is picked up on the next pass.
//   - The engine never waits for it and never reads from it. A database
//     outage makes history stale (lag grows) and nothing else.
//   - It is disposable: drop the database, and it is rebuilt from the
//     journal, identically.
//   - Reconcile compares SQL with the replay state at the SAME Seq.
package projector
