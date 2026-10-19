package state

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// CheckInvariants verifies every money and order rule and returns the first
// one broken, or nil. It is O(balances + orders): meant for tests, fuzzing
// and the replay tool, never the hot path.
//
// The rules:
//  1. The order book's own invariants hold (book.CheckInvariants).
//  2. Conservation: for each asset, all balances (including the negative
//     issuance account) sum to exactly zero.
//  3. Users never hold a negative balance, and only the system holds
//     issuance. Total issued never exceeds the asset's MaxSupply.
//  4. No zero balance is stored (one canonical form).
//  5. The open orders State tracks are exactly the orders resting in the
//     book, with the same owner, side and price.
//  6. Reserve function: each open order's reservation equals what its
//     remaining quantity requires (buy: notional at its limit; sell: lots).
//  7. Each user's Reserved bucket equals the sum of their open orders'
//     reservations in that asset. No money is reserved for nothing.
//  8. No open order has an ID above the last ID assigned.
func (s *State) CheckInvariants() error {
	if err := s.book.CheckInvariants(); err != nil { // rule 1
		return fmt.Errorf("book: %w", err)
	}

	sums := map[domain.Asset]int64{}
	for k, v := range s.balances {
		switch {
		case !k.asset.Valid():
			return fmt.Errorf("balance in invalid asset %v", k.asset)
		case v == 0: // rule 4
			return fmt.Errorf("zero balance stored for %+v", k)
		case k.bucket == Issuance && k.user != SystemUser: // rule 3
			return fmt.Errorf("user %d holds an issuance account", k.user)
		case k.bucket != Issuance && k.user == SystemUser:
			return fmt.Errorf("the system user holds a %v balance", k.bucket)
		case k.bucket == Issuance && (v > 0 || -v > k.asset.MaxSupply()):
			return fmt.Errorf("issued %v %d is outside 0..MaxSupply", k.asset, -v)
		case k.bucket != Issuance && v < 0:
			return fmt.Errorf("user %d has a negative %v %v balance: %d", k.user, k.asset, k.bucket, v)
		}
		sums[k.asset] += v
	}
	for asset, sum := range sums { // rule 2
		if sum != 0 {
			return fmt.Errorf("%v does not balance: all accounts sum to %d", asset, sum)
		}
	}

	if len(s.open) != s.book.Len() { // rule 5
		return fmt.Errorf("state tracks %d open orders, book holds %d", len(s.open), s.book.Len())
	}
	reservedByUser := map[balanceKey]int64{}
	for id, o := range s.open {
		bo, ok := s.book.Get(id)
		switch {
		case !ok:
			return fmt.Errorf("open order %d is not in the book", id)
		case bo.User != o.user || bo.Side != o.side || bo.Price != o.price:
			return fmt.Errorf("open order %d disagrees with the book: state %+v, book %+v", id, o, bo)
		case id > s.lastOrder: // rule 8
			return fmt.Errorf("open order %d is above the last assigned ID %d", id, s.lastOrder)
		}
		asset, want := reserveFor(o.side, o.price, bo.Qty) // rule 6
		if o.reserved != want {
			return fmt.Errorf("order %d reserves %d %v, but %s left at %d requires %d",
				id, o.reserved, asset, domain.FormatQty(bo.Qty), o.price, want)
		}
		reservedByUser[balanceKey{o.user, asset, Reserved}] += o.reserved
	}
	for k, v := range s.balances { // rule 7, in one direction...
		if k.bucket == Reserved && v != reservedByUser[k] {
			return fmt.Errorf("user %d has %d %v reserved, but open orders need %d", k.user, v, k.asset, reservedByUser[k])
		}
	}
	for k, v := range reservedByUser { // ...and the other
		if s.balances[k] != v {
			return fmt.Errorf("user %d's open orders need %d %v reserved, but the bucket holds %d", k.user, v, k.asset, s.balances[k])
		}
	}
	return nil
}
