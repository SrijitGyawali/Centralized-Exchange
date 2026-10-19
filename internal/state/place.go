package state

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// place reserves funds for a new order, matches it, settles every fill, and
// releases whatever reservation is no longer needed. All of it is one
// transition.
//
// Step by step, for "buy 0.1 BTC, limit 60,100" against an ask at 60,000:
//
//  1. Validate everything (fields, then funds). A rejection changes NOTHING:
//     no journal, no order ID used.
//  2. Reservation: hold 0.1 × 60,100 = 6,010 USDT (available → reserved).
//     A buy reserves at its LIMIT price, the most it could possibly pay.
//  3. Match in the book; for each fill, Settlement: the buyer's reserved
//     USDT goes to the seller, the seller's reserved BTC goes to the buyer.
//     Here 0.1 at 60,000 costs 6,000.
//  4. Release: whatever the taker reserved beyond what its resting
//     remainder still needs goes back to available. Here 6,010 - 6,000 = 10
//     USDT of price improvement. An IOC or self-trade remainder releases
//     everything left.
//
// After step 1 nothing can fail. Balances cannot overflow (supply cap) and
// cannot go negative (funds were checked), so an order is either rejected
// untouched or applied completely.
func (s *State) place(t *tx, user domain.UserID, p command.PlaceOrder) command.Result {
	// 1. Validate. These match the book's own checks; if they ever drifted
	// apart, book.Place would return an error below and we would panic, and
	// the random tests would find it.
	switch {
	case user == SystemUser:
		return reject(command.RejectInvalid, "order: user must be non-zero")
	case !p.Side.Valid():
		return reject(command.RejectInvalid, "order: side must be buy or sell")
	case !p.TIF.Valid():
		return reject(command.RejectInvalid, "order: time in force must be gtc or ioc")
	case p.Price < domain.MinPrice || p.Price > domain.MaxPrice:
		return reject(command.RejectInvalid, "order: price %d outside %d..%d", p.Price, domain.MinPrice, domain.MaxPrice)
	case p.Qty < domain.MinQty || p.Qty > domain.MaxQty:
		return reject(command.RejectInvalid, "order: qty %d lots outside %d..%d", p.Qty, domain.MinQty, domain.MaxQty)
	}
	asset, need := reserveFor(p.Side, p.Price, p.Qty)
	if avail, _ := s.Balance(user, asset); avail < need {
		return reject(command.RejectInsufficientFunds, "order needs %s %v, available %s",
			asset.Format(need), asset, asset.Format(avail))
	}

	// 2. Reserve. The order gets its ID only now, so rejections never use one.
	s.lastOrder++
	id := s.lastOrder
	t.post(Reservation, id, 0, move(user, asset, Available, Reserved, need)...)

	// 3. Match and settle.
	res, err := s.book.Place(book.Order{ID: id, User: user, Side: p.Side, Price: p.Price, Qty: p.Qty, Seq: t.seq}, p.TIF)
	if err != nil {
		panic(fmt.Sprintf("state: book rejected a validated order: %v", err))
	}
	takerReserve := need
	for _, f := range res.Fills {
		takerReserve -= s.settle(t, f)
	}

	// 4. Keep what a resting remainder needs; release the rest.
	var keep int64
	if res.Outcome == book.Rested {
		_, keep = reserveFor(p.Side, p.Price, res.Remaining)
		s.open[id] = openOrder{user: user, side: p.Side, price: p.Price, reserved: keep}
	}
	switch release := takerReserve - keep; {
	case release > 0:
		t.post(Release, id, 0, move(user, asset, Reserved, Available, release)...)
	case release < 0:
		panic(fmt.Sprintf("state: order %d spent %d more than it reserved", id, -release))
	}

	return command.Result{Order: id, Outcome: res.Outcome, Fills: res.Fills, Remaining: res.Remaining}
}

// settle moves the money for one fill and returns how much of the TAKER's
// reservation it used up (USDT atoms for a buying taker, BTC lots for a
// selling one).
//
//	Settlement journal (0.1 BTC at 60,000 → cost 6,000 USDT):
//	  USDT  buyer   reserved   -6,000
//	  USDT  seller  available  +6,000
//	  BTC   seller  reserved   -0.1
//	  BTC   buyer   available  +0.1
//
// The fill price is always the MAKER's price. A resting buy maker therefore
// pays exactly its own limit, so its reservation shrinks by exactly the cost
// and nothing is ever left over when it fills completely.
func (s *State) settle(t *tx, f book.Trade) int64 {
	cost := int64(mustNotional(f.Price, f.Qty))
	q := int64(f.Qty)

	buyer, seller := f.TakerUser, f.MakerUser
	if f.TakerSide == domain.Sell {
		buyer, seller = seller, buyer
	}
	t.post(Settlement, f.Taker, f.Maker,
		Posting{Account{buyer, Reserved}, domain.USDT, -cost},
		Posting{Account{seller, Available}, domain.USDT, cost},
		Posting{Account{seller, Reserved}, domain.BTC, -q},
		Posting{Account{buyer, Available}, domain.BTC, q},
	)

	// The maker's reservation shrinks by what it gave up.
	m := s.open[f.Maker]
	takerUsed := cost
	if f.TakerSide == domain.Buy { // the maker is a seller: it gave BTC
		m.reserved -= q
	} else { // the maker is a buyer: it paid USDT
		m.reserved -= cost
		takerUsed = q
	}
	if f.MakerRemaining == 0 {
		if m.reserved != 0 {
			panic(fmt.Sprintf("state: filled maker %d still reserves %d", f.Maker, m.reserved))
		}
		delete(s.open, f.Maker)
	} else {
		s.open[f.Maker] = m
	}
	return takerUsed
}
