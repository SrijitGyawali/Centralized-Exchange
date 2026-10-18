package book

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// Trade is one fill: an incoming order (the taker) trading against a resting
// order (the maker).
//
// Every field is a plain value, so a Trade can be journaled, compared,
// projected into SQL or sent to a client without touching the book.
type Trade struct {
	Price domain.Price // always the MAKER's price (see Place)
	Qty   domain.Qty   // lots traded in this fill

	Maker          domain.OrderID
	MakerUser      domain.UserID
	MakerRemaining domain.Qty // maker's quantity left after this fill; 0 = fully filled and gone

	Taker     domain.OrderID
	TakerUser domain.UserID
	TakerSide domain.Side // the aggressor: Buy means a buyer lifted an ask
}

// Outcome says what happened to the incoming order once matching stopped.
type Outcome uint8

const (
	_ Outcome = iota // zero value: no outcome yet (never returned on success)

	// Filled: the whole order traded; nothing is left.
	Filled
	// Rested: a GTC order's unfilled remainder (possibly all of it) now
	// rests in the book.
	Rested
	// CanceledIOC: an IOC order's unfilled remainder was canceled.
	CanceledIOC
	// CanceledSelfTrade: matching reached a resting order from the same
	// user, so it stopped and the remainder was canceled (any earlier fills
	// against other users stand).
	CanceledSelfTrade
)

// String implements fmt.Stringer.
func (o Outcome) String() string {
	switch o {
	case Filled:
		return "filled"
	case Rested:
		return "rested"
	case CanceledIOC:
		return "canceled (ioc remainder)"
	case CanceledSelfTrade:
		return "canceled (self-trade prevented)"
	default:
		return fmt.Sprintf("Outcome(%d)", uint8(o))
	}
}

// Result is everything Place did to the incoming order.
type Result struct {
	Fills     []Trade    // in execution order: best price first, FIFO within a price
	Remaining domain.Qty // quantity that did not trade (0 when Filled)
	Outcome   Outcome
}

// FilledQty returns the total quantity traded across all fills. It always
// equals the order's Qty minus Remaining.
func (r Result) FilledQty() domain.Qty {
	var q domain.Qty
	for _, f := range r.Fills {
		q += f.Qty
	}
	return q
}

// Place submits order o: it trades against the opposite side for as long as
// prices cross, then deals with any remainder according to tif.
//
// The rules (plan §07, the Tier 1 matching contract):
//
//  1. Price priority: the best opposite price trades first (lowest ask for
//     a buy, highest bid for a sell).
//  2. Time priority: at one price, the oldest order (lowest Seq) trades
//     first. A partially filled maker keeps its place in the queue.
//  3. Maker price: every fill executes at the RESTING order's price. A buy
//     limited at 103 that meets asks at 100 and 101 pays 100 and 101. The
//     taker gets "price improvement", never a worse price than its limit.
//  4. Prices cross while ask <= buy limit, or bid >= sell limit.
//  5. Self-trade prevention: if the next maker in line belongs to the same
//     user, matching stops and the remainder is canceled. Fills already
//     made against other users stand. A user's own orders are never touched.
//  6. Remainder: GTC rests it at its limit price, at the back of that queue.
//     IOC cancels it.
//
// Validation happens before anything changes (checkNew). After that,
// matching cannot fail, so Place either rejects the order and changes
// nothing, or applies it completely. There is no in-between.
//
// Every accepted order consumes its Seq, even if it fills completely or is
// canceled and never rests.
func (b *Book) Place(o Order, tif domain.TimeInForce) (Result, error) {
	if !tif.Valid() {
		return Result{}, fmt.Errorf("%w: time in force %v", ErrInvalidOrder, tif)
	}
	if err := b.checkNew(o); err != nil {
		return Result{}, err
	}

	// All checks passed. From here on nothing can fail.
	b.lastSeq = o.Seq
	res := Result{Remaining: o.Qty}
	opposite := b.sideOf(o.Side.Opposite())

	for res.Remaining > 0 {
		best := opposite.best()
		if best == nil || !crosses(o.Side, o.Price, best.price) {
			break // opposite side empty, or its best price is beyond our limit
		}
		maker := best.head // oldest order at the best price
		if maker.order.User == o.User {
			res.Outcome = CanceledSelfTrade
			break
		}

		qty := min(res.Remaining, maker.order.Qty)
		maker.order.Qty -= qty
		best.total -= qty
		res.Remaining -= qty
		res.Fills = append(res.Fills, Trade{
			Price:          best.price,
			Qty:            qty,
			Maker:          maker.order.ID,
			MakerUser:      maker.order.User,
			MakerRemaining: maker.order.Qty,
			Taker:          o.ID,
			TakerUser:      o.User,
			TakerSide:      o.Side,
		})

		if maker.order.Qty == 0 { // maker fully filled: it leaves the book
			best.remove(maker)
			delete(b.orders, maker.order.ID)
			if best.empty() {
				opposite.removeLevel(best)
			}
		}
	}

	switch {
	case res.Outcome == CanceledSelfTrade:
		// Remainder dropped. Nothing rests.
	case res.Remaining == 0:
		res.Outcome = Filled
	case tif == domain.IOC:
		res.Outcome = CanceledIOC
	default: // GTC with quantity left. It no longer crosses, so it can rest.
		o.Qty = res.Remaining
		b.rest(o)
		res.Outcome = Rested
	}
	return res, nil
}

// crosses reports whether an order on side s with limit price limit would
// trade against a resting order at makerPrice.
func crosses(s domain.Side, limit, makerPrice domain.Price) bool {
	if s == domain.Buy {
		return makerPrice <= limit
	}
	return makerPrice >= limit
}
