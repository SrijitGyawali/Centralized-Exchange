// Package state holds the complete trading state and the single deterministic
// transition function that changes it:
//
//	func (s *State) Apply(cmd command.Command) command.Result
//
// State contains every book, every account (available and reserved per
// asset), per-order reservations, the double-entry ledger, the idempotency
// (dedupe) table, ID counters and the versioned symbol configuration.
// Together these form one transaction boundary: one Apply call is one atomic
// transition. See docs/adr/0001-scope-and-single-writer.md.
//
// # Apply order (week 3)
//
//  1. Dedupe: the same key and hash returns the saved result; the same key
//     with a different hash is a conflict.
//  2. Validate and run risk checks. Compute every affected amount *before*
//     mutating anything, so a rejection leaves no partial effect.
//  3. Reserve funds, match in the book, settle fills, write ledger postings.
//  4. Record the result for dedupe and return it with ordered events.
//
// # Rules
//
//   - Deterministic: no disk, network, clock, randomness or map-order
//     dependence. Replaying the same commands must produce byte-identical
//     state, which a canonical SHA-256 digest of the full state will verify.
//   - Invariants checked in tests after every command: per-asset postings
//     sum to zero, user balances are >= 0, reservations match live orders,
//     and the book is never crossed.
package state
