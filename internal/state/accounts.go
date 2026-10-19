package state

import (
	"fmt"
	"math"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// tx collects the journals one command produces, in order. Every balance
// change goes through tx.post, which is the only code that writes
// State.balances. So the ledger and the balances can never disagree: the
// balances are, by construction, the sum of every posting ever made.
type tx struct {
	s        *State
	seq      domain.Seq
	journals []Journal
}

// post records one journal and applies its postings to the balances.
//
// It panics if the journal is unbalanced, or if a posting would overflow or
// make a user's balance negative. Callers validate first (funds available,
// supply cap respected), so these checks can only fail through a bug. When
// money is wrong, halting is better than continuing. On restart, replaying
// the journal will reproduce the same panic at the same command, pointing
// straight at the bug.
func (t *tx) post(kind JournalKind, order, maker domain.OrderID, postings ...Posting) {
	j := Journal{Seq: t.seq, Index: len(t.journals), Kind: kind, Order: order, Maker: maker, Postings: postings}
	if !j.Balanced() {
		panic(fmt.Sprintf("state: unbalanced %v journal: %+v", kind, postings))
	}
	for _, p := range postings {
		t.s.apply(p)
	}
	t.journals = append(t.journals, j)
}

// apply adds one posting to its account's balance.
func (s *State) apply(p Posting) {
	k := balanceKey{p.Account.User, p.Asset, p.Account.Bucket}
	cur := s.balances[k]
	if (p.Amount > 0 && cur > math.MaxInt64-p.Amount) || (p.Amount < 0 && cur < math.MinInt64-p.Amount) {
		panic(fmt.Sprintf("state: balance overflow on %+v", p))
	}
	next := cur + p.Amount
	if next < 0 && p.Account.Bucket != Issuance {
		panic(fmt.Sprintf("state: posting %+v would make a balance negative (%d)", p, next))
	}
	if next == 0 {
		delete(s.balances, k) // keep only non-zero balances: one canonical form
		return
	}
	s.balances[k] = next
}

// move returns the two postings that shift amount of asset between two
// buckets of the same user, e.g. Available → Reserved for a reservation.
func move(user domain.UserID, asset domain.Asset, from, to Bucket, amount int64) []Posting {
	return []Posting{
		{Account: Account{User: user, Bucket: from}, Asset: asset, Amount: -amount},
		{Account: Account{User: user, Bucket: to}, Asset: asset, Amount: amount},
	}
}
