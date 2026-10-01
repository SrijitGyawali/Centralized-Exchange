// Package command defines the data-only messages that change exchange state,
// and the results they produce.
//
// A command is what gets written to the journal and replayed after a crash,
// so it must contain everything Apply needs and nothing that cannot be
// serialized:
//
//   - PlaceOrder, CancelOrder, Fund (test funding), and later operator
//     commands such as Halt.
//   - Every command carries the user, the client idempotency key, a hash of
//     the normalized request, and (once sequenced) Seq and a server
//     timestamp.
//   - Result types describe the deterministic outcome: accepted, filled,
//     resting, canceled, or a typed business rejection.
//
// # Why a separate package
//
// api builds commands, engine sequences them, journal encodes them and state
// applies them. If commands lived in state, journal would have to import
// state. Keeping them in their own leaf package keeps the dependency graph
// simple and acyclic.
//
// # Rules
//
//   - Plain values only: no channels, no funcs, no pointers into live state.
//     The engine keeps the reply channel next to the command, never inside it.
package command
