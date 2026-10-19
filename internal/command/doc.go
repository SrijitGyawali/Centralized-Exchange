// Package command defines the data-only messages that change exchange state,
// and the results they produce.
//
// A command is what gets written to the journal and replayed after a crash,
// so it must contain everything Apply needs and nothing that cannot be
// serialized:
//
//   - Command: Seq (assigned by the engine), User, an idempotency Key, and a
//     Body saying what to do.
//   - Bodies: PlaceOrder, CancelOrder and Fund (test funding). Body is a
//     sealed interface, so these three are the complete list.
//   - Result: the deterministic outcome. Accepted, or a typed RejectReason
//     such as insufficient funds; the assigned order ID, the outcome, fills
//     and remaining quantity; whether it was a duplicate.
//
// # Why a separate package
//
// api builds commands, engine sequences them, journal encodes them and state
// applies them. If commands lived in state, journal would have to import
// state. As its own small package, command depends only on domain and book's
// plain data types (Trade, Outcome), and the dependency graph stays simple
// and acyclic.
//
// # Rules
//
//   - Plain values only: no channels, no funcs, no pointers into live state.
//     The engine keeps the reply channel next to the command, never inside it.
//   - Bodies stay comparable: deduplication compares a retried request with
//     the original using ==, which is exact and needs no hashing.
//   - Business rejections are Results, not Go errors. They are decisions,
//     remembered and replayed like any other outcome.
package command
