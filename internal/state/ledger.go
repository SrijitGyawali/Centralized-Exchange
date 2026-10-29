package state

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// The ledger uses double-entry bookkeeping. Money never appears or vanishes:
// every change is a Journal whose Postings move amounts between accounts,
// and for each asset the postings sum to exactly zero.
//
//	Alice buys 0.1 BTC from Bob at 60,000 (one Trade journal):
//
//	  USDT  Alice  reserved   -6000.000000
//	  USDT  Bob    available  +6000.000000   USDT sums to 0
//	  BTC   Bob    reserved      -0.1000
//	  BTC   Alice  available     +0.1000     BTC sums to 0
//
// BTC and USDT are never added together; each asset must balance on its own.

// Bucket is which part of an owner's money an account holds.
type Bucket uint8

const (
	_ Bucket = iota
	// Available: free to place orders with.
	Available
	// Reserved: held for the owner's open orders, so it cannot be spent
	// twice. Placing an order moves funds Available → Reserved; filling or
	// canceling it moves them out again.
	Reserved
	// Issuance: system-only. The other side of every test-funding journal.
	// It goes NEGATIVE as money is created: -issuance equals the total
	// supply ever funded, which is what lets every asset still sum to zero.
	Issuance
)

// String implements fmt.Stringer.
func (b Bucket) String() string {
	switch b {
	case Available:
		return "available"
	case Reserved:
		return "reserved"
	case Issuance:
		return "issuance"
	default:
		return fmt.Sprintf("Bucket(%d)", uint8(b))
	}
}

// SystemUser owns the Issuance accounts. Real users start at 1.
const SystemUser domain.UserID = 0

// Account identifies one balance holder: a user's bucket, or the system's
// issuance account (SystemUser, Issuance).
type Account struct {
	User   domain.UserID
	Bucket Bucket
}

// Posting is one signed line of a journal: Amount (in the asset's smallest
// unit) is added to the account's balance. Positive increases it, negative
// decreases it.
type Posting struct {
	Account Account
	Asset   domain.Asset
	Amount  int64
}

// JournalKind says which business event a journal records.
type JournalKind uint8

const (
	_ JournalKind = iota
	// Funding: test money created (issuance → user available).
	Funding
	// Reservation: an order's funds held (available → reserved).
	Reservation
	// Settlement: one fill (buyer's reserved USDT → seller; seller's
	// reserved BTC → buyer).
	Settlement
	// Release: unneeded reservation returned (reserved → available), after
	// price improvement, a canceled remainder, or a cancel.
	Release
)

// String implements fmt.Stringer.
func (k JournalKind) String() string {
	switch k {
	case Funding:
		return "funding"
	case Reservation:
		return "reservation"
	case Settlement:
		return "settlement"
	case Release:
		return "release"
	default:
		return fmt.Sprintf("JournalKind(%d)", uint8(k))
	}
}

// Journal is one balanced business event.
//
// (Seq, Index) is its stable, unique identity: the command that produced it
// and its position among that command's journals. Replaying the same
// commands always produces the same journals with the same identities.
// The SQL projector uses that pair as a primary key, so delivering
// a journal twice is harmless.
type Journal struct {
	Seq      domain.Seq
	Index    int
	Kind     JournalKind
	Order    domain.OrderID // the order this is about (the taker, for a settlement)
	Maker    domain.OrderID // settlements only: the resting order that was hit
	Postings []Posting
}

// Balanced reports whether, for every asset, the postings sum to zero.
func (j Journal) Balanced() bool {
	var sums [3]int64 // indexed by Asset: 0 unused, BTC, USDT
	for _, p := range j.Postings {
		if !p.Asset.Valid() {
			return false
		}
		sums[p.Asset] += p.Amount
	}
	return sums[domain.BTC] == 0 && sums[domain.USDT] == 0
}
