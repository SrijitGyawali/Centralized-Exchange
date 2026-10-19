// Package state holds the complete trading state and the single deterministic
// transition function that changes it:
//
//	func (s *State) Apply(cmd command.Command) (Effects, error)
//
// State contains the order book, every balance (available, reserved, and the
// system's issuance account, per asset), each open order's reservation, the
// idempotency (dedupe) table and the ID counters. Together they form one
// transaction boundary: one Apply call is one atomic transition of all of
// them. See docs/adr/0001-scope-and-single-writer.md and
// docs/adr/0004-accounting-reservations-ledger-idempotency.md.
//
// # The money flow of one order
//
//	Reservation  available → reserved      (buy: notional at its limit; sell: the BTC)
//	Settlement   per fill: buyer's reserved USDT → seller, seller's reserved BTC → buyer
//	Release      reserved → available      (price improvement, canceled remainder, cancel)
//
// Every change is a balanced Journal (postings per asset sum to zero),
// returned in Effects. The balances are exactly the sum of every posting ever
// made, because tx.post is the only code that changes them.
//
// # Apply order
//
//  1. Sequencing (Seq must increase; the body must be a known kind). Bad
//     sequencing is a Go error and leaves the state untouched.
//  2. Idempotency key validation.
//  3. Dedupe by (user, key): the same request returns its stored result; a
//     different request under the same key is a key conflict.
//  4. Execute (fund / place / cancel). Every check runs before any change.
//  5. Remember the result, accepted or rejected.
//
// # Rules
//
//   - Deterministic: no disk, network, clock, randomness or map-order
//     dependence. Replaying the same commands reproduces the same results,
//     journals and state exactly (tested by replaying 3,000 random commands).
//   - Invariants (CheckInvariants): per-asset conservation, no negative user
//     balances, issuance within MaxSupply, every open order's reservation
//     equals the reserve function, reserved buckets equal the sum of open
//     orders' reservations, and the book's own invariants.
//   - "Cannot happen" conditions (an unbalanced journal, a negative balance,
//     a book rejecting a validated order) panic. When money is wrong,
//     stopping is safer than continuing.
package state
