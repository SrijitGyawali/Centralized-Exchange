package state

import (
	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// State is the complete trading state: the order book, every balance, every
// open order's reservation, the deduplication table and the ID counters.
// Together they form ONE transaction boundary: one Apply call is one atomic
// transition of all of them (ADR-0001).
//
// Like Book, State is not safe for concurrent use, on purpose. The engine's
// single owner goroutine is the only code that touches it.
type State struct {
	book *book.Book

	// balances holds every non-zero balance, keyed by owner, asset and
	// bucket. Zero balances are deleted, so the map stays small and has a
	// single canonical form, which week 4's state digest will need.
	balances map[balanceKey]int64

	// open tracks how much each resting order has reserved. The book knows
	// an order's remaining quantity; State knows the money behind it.
	open map[domain.OrderID]openOrder

	// dedupe remembers the result of every request by (user, key), so a
	// retried request returns its original result instead of running twice.
	dedupe map[dedupeKey]dedupeEntry

	lastOrder domain.OrderID // the last order ID assigned; IDs are never reused
	lastSeq   domain.Seq     // the last command applied
}

type balanceKey struct {
	user   domain.UserID
	asset  domain.Asset
	bucket Bucket
}

// openOrder is the money side of a resting order. reserved is in the asset
// the order spends: USDT atoms for a buy, BTC lots for a sell.
type openOrder struct {
	user     domain.UserID
	side     domain.Side
	price    domain.Price
	reserved int64
}

type dedupeKey struct {
	user domain.UserID
	key  string
}

type dedupeEntry struct {
	body   command.Body   // the original request, compared with == on retry
	result command.Result // its outcome, returned (as a copy) on retry
}

// New returns an empty state: no money, no orders, Seq 0.
func New() *State {
	return &State{
		book:     book.New(),
		balances: make(map[balanceKey]int64),
		open:     make(map[domain.OrderID]openOrder),
		dedupe:   make(map[dedupeKey]dedupeEntry),
	}
}

// Balance returns a user's available and reserved amounts of an asset, in
// its smallest unit (lots or atoms).
func (s *State) Balance(user domain.UserID, asset domain.Asset) (available, reserved int64) {
	return s.balances[balanceKey{user, asset, Available}], s.balances[balanceKey{user, asset, Reserved}]
}

// Issued returns the total amount of an asset created by funding so far.
func (s *State) Issued(asset domain.Asset) int64 {
	return -s.balances[balanceKey{SystemUser, asset, Issuance}]
}

// Order returns a copy of a resting order as the book holds it.
func (s *State) Order(id domain.OrderID) (book.Order, bool) { return s.book.Get(id) }

// Depth returns a copy of the top n book levels per side (n <= 0: all).
func (s *State) Depth(n int) book.Depth { return s.book.Depth(n) }

// LastSeq returns the Seq of the last command applied.
func (s *State) LastSeq() domain.Seq { return s.lastSeq }

// reserveFor returns what an order must hold while it rests: for a buy, the
// USDT notional of its remaining quantity at its LIMIT price; for a sell,
// its remaining BTC lots. This is the "reserve function" every open order's
// reservation must equal at all times (checked by CheckInvariants).
//
// Notional cannot fail here: price and qty are already validated, so the
// product is at most 1e13 atoms.
func reserveFor(side domain.Side, price domain.Price, qty domain.Qty) (domain.Asset, int64) {
	if side == domain.Sell {
		return domain.BTC, int64(qty)
	}
	return domain.USDT, int64(mustNotional(price, qty))
}

// mustNotional is domain.Notional for inputs already known to be valid. An
// error would mean a bug in validation, so it panics instead of letting a
// wrong amount reach the ledger.
func mustNotional(price domain.Price, qty domain.Qty) domain.Atoms {
	n, err := domain.Notional(price, qty)
	if err != nil {
		panic("state: notional of validated order failed: " + err.Error())
	}
	return n
}
