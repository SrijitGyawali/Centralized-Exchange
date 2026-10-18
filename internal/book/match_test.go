package book

import (
	"errors"
	"slices"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// mk builds an order for a specific user (self-trade tests need several
// users). Seq = id, so orders listed in id order have increasing Seq.
func mk(id, user uint64, side domain.Side, price, qty int64) Order {
	o := ord(id, side, price, qty)
	o.User = domain.UserID(user)
	return o
}

// fill is the part of an expected Trade a test case spells out: which maker
// was hit, at what price, for how much, and what the maker had left. The
// test fills in the maker's user and every taker field from the case itself.
type fill struct {
	maker     domain.OrderID
	price     domain.Price
	qty       domain.Qty
	makerLeft domain.Qty
}

func lv(price, qty int64, orders int) LevelSummary {
	return LevelSummary{Price: domain.Price(price), Qty: domain.Qty(qty), Orders: orders}
}

// TestPlaceMatching is the matching rulebook as a table: each row is one
// scenario with the resting orders, one incoming order, and exactly which
// trades and which final book must result.
func TestPlaceMatching(t *testing.T) {
	const me = 9 // the taker's user in every case

	tests := []struct {
		name          string
		resting       []Order // placed first as GTC; must all rest
		taker         Order
		tif           domain.TimeInForce
		wantFills     []fill
		wantRemaining domain.Qty
		wantOutcome   Outcome
		wantBids      []LevelSummary // best first
		wantAsks      []LevelSummary // best first
	}{
		{
			name:          "empty book: gtc rests",
			taker:         mk(10, me, domain.Buy, 100, 1000),
			tif:           domain.GTC,
			wantRemaining: 1000, wantOutcome: Rested,
			wantBids: []LevelSummary{lv(100, 1000, 1)},
		},
		{
			name:          "empty book: ioc is canceled whole",
			taker:         mk(10, me, domain.Buy, 100, 1000),
			tif:           domain.IOC,
			wantRemaining: 1000, wantOutcome: CanceledIOC,
		},
		{
			name:          "below the best ask: no trade, rests",
			resting:       []Order{mk(1, 1, domain.Sell, 101, 1000)},
			taker:         mk(10, me, domain.Buy, 100, 1000),
			tif:           domain.GTC,
			wantRemaining: 1000, wantOutcome: Rested,
			wantBids: []LevelSummary{lv(100, 1000, 1)},
			wantAsks: []LevelSummary{lv(101, 1000, 1)},
		},
		{
			name:        "exact full fill",
			resting:     []Order{mk(1, 1, domain.Sell, 100, 1000)},
			taker:       mk(10, me, domain.Buy, 100, 1000),
			tif:         domain.GTC,
			wantFills:   []fill{{1, 100, 1000, 0}},
			wantOutcome: Filled,
		},
		{
			name:        "taker smaller than maker: maker partially filled",
			resting:     []Order{mk(1, 1, domain.Sell, 100, 3000), mk(2, 2, domain.Sell, 100, 1000)},
			taker:       mk(10, me, domain.Buy, 100, 1000),
			tif:         domain.GTC,
			wantFills:   []fill{{1, 100, 1000, 2000}},
			wantOutcome: Filled,
			wantAsks:    []LevelSummary{lv(100, 3000, 2)},
		},
		{
			name:          "taker larger than maker: gtc remainder rests at its limit",
			resting:       []Order{mk(1, 1, domain.Sell, 100, 1000)},
			taker:         mk(10, me, domain.Buy, 101, 2500),
			tif:           domain.GTC,
			wantFills:     []fill{{1, 100, 1000, 0}},
			wantRemaining: 1500, wantOutcome: Rested,
			wantBids: []LevelSummary{lv(101, 1500, 1)},
		},
		{
			name: "sweep three levels, each at the maker's price",
			resting: []Order{
				mk(1, 1, domain.Sell, 100, 1000), mk(2, 2, domain.Sell, 101, 1000),
				mk(3, 3, domain.Sell, 102, 1000), mk(4, 4, domain.Sell, 104, 1000),
			},
			taker:       mk(10, me, domain.Buy, 103, 2500), // willing to pay up to 103
			tif:         domain.GTC,
			wantFills:   []fill{{1, 100, 1000, 0}, {2, 101, 1000, 0}, {3, 102, 500, 500}},
			wantOutcome: Filled,
			wantAsks:    []LevelSummary{lv(102, 500, 1), lv(104, 1000, 1)},
		},
		{
			name:          "limit price stops the sweep; remainder rests",
			resting:       []Order{mk(1, 1, domain.Sell, 100, 1000), mk(2, 2, domain.Sell, 102, 1000)},
			taker:         mk(10, me, domain.Buy, 101, 3000),
			tif:           domain.GTC,
			wantFills:     []fill{{1, 100, 1000, 0}},
			wantRemaining: 2000, wantOutcome: Rested,
			wantBids: []LevelSummary{lv(101, 2000, 1)},
			wantAsks: []LevelSummary{lv(102, 1000, 1)},
		},
		{
			name: "same price: oldest maker fills first (FIFO)",
			resting: []Order{
				mk(1, 1, domain.Sell, 100, 1000), mk(2, 2, domain.Sell, 100, 1000), mk(3, 3, domain.Sell, 100, 1000),
			},
			taker:       mk(10, me, domain.Buy, 100, 1500),
			tif:         domain.GTC,
			wantFills:   []fill{{1, 100, 1000, 0}, {2, 100, 500, 500}},
			wantOutcome: Filled,
			wantAsks:    []LevelSummary{lv(100, 1500, 2)},
		},
		{
			name:          "ioc fills what it can, cancels the rest",
			resting:       []Order{mk(1, 1, domain.Sell, 100, 1000)},
			taker:         mk(10, me, domain.Buy, 105, 3000),
			tif:           domain.IOC,
			wantFills:     []fill{{1, 100, 1000, 0}},
			wantRemaining: 2000, wantOutcome: CanceledIOC,
		},
		{
			name: "sell sweeps bids, highest first",
			resting: []Order{
				mk(1, 1, domain.Buy, 100, 1000), mk(2, 2, domain.Buy, 99, 1000), mk(3, 3, domain.Buy, 98, 1000),
			},
			taker:       mk(10, me, domain.Sell, 99, 1500), // willing to sell down to 99
			tif:         domain.GTC,
			wantFills:   []fill{{1, 100, 1000, 0}, {2, 99, 500, 500}},
			wantOutcome: Filled,
			wantBids:    []LevelSummary{lv(99, 500, 1), lv(98, 1000, 1)},
		},
		{
			name: "self-trade: stop at own order, keep earlier fills, cancel the rest",
			resting: []Order{
				mk(1, 1, domain.Sell, 100, 1000), mk(2, me, domain.Sell, 101, 1000), mk(3, 3, domain.Sell, 102, 1000),
			},
			taker:         mk(10, me, domain.Buy, 102, 3000),
			tif:           domain.GTC, // even GTC: the remainder does not rest
			wantFills:     []fill{{1, 100, 1000, 0}},
			wantRemaining: 2000, wantOutcome: CanceledSelfTrade,
			wantAsks: []LevelSummary{lv(101, 1000, 1), lv(102, 1000, 1)}, // own order untouched
		},
		{
			name:          "self-trade on the very first maker: nothing trades",
			resting:       []Order{mk(1, me, domain.Sell, 100, 1000)},
			taker:         mk(10, me, domain.Buy, 100, 1000),
			tif:           domain.GTC,
			wantRemaining: 1000, wantOutcome: CanceledSelfTrade,
			wantAsks: []LevelSummary{lv(100, 1000, 1)},
		},
		{
			name:          "self-trade inside one level, after a fill",
			resting:       []Order{mk(1, 1, domain.Sell, 100, 1000), mk(2, me, domain.Sell, 100, 1000)},
			taker:         mk(10, me, domain.Buy, 100, 2000),
			tif:           domain.IOC,
			wantFills:     []fill{{1, 100, 1000, 0}},
			wantRemaining: 1000, wantOutcome: CanceledSelfTrade,
			wantAsks: []LevelSummary{lv(100, 1000, 1)},
		},
		{
			name:          "own order beyond the limit price is not a self-trade",
			resting:       []Order{mk(1, 1, domain.Sell, 100, 1000), mk(2, me, domain.Sell, 101, 1000)},
			taker:         mk(10, me, domain.Buy, 100, 2000),
			tif:           domain.GTC,
			wantFills:     []fill{{1, 100, 1000, 0}},
			wantRemaining: 1000, wantOutcome: Rested,
			wantBids: []LevelSummary{lv(100, 1000, 1)},
			wantAsks: []LevelSummary{lv(101, 1000, 1)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := New()
			mustAdd(t, b, tc.resting...)
			users := map[domain.OrderID]domain.UserID{}
			for _, o := range tc.resting {
				users[o.ID] = o.User
			}

			got, err := b.Place(tc.taker, tc.tif)
			if err != nil {
				t.Fatalf("Place: %v", err)
			}
			mustBeValid(t, b)

			var wantTrades []Trade
			for _, f := range tc.wantFills {
				wantTrades = append(wantTrades, Trade{
					Price: f.price, Qty: f.qty,
					Maker: f.maker, MakerUser: users[f.maker], MakerRemaining: f.makerLeft,
					Taker: tc.taker.ID, TakerUser: tc.taker.User, TakerSide: tc.taker.Side,
				})
			}
			if !slices.Equal(got.Fills, wantTrades) {
				t.Errorf("fills:\n got  %+v\n want %+v", got.Fills, wantTrades)
			}
			if got.Remaining != tc.wantRemaining || got.Outcome != tc.wantOutcome {
				t.Errorf("remaining %d, outcome %v; want %d, %v", got.Remaining, got.Outcome, tc.wantRemaining, tc.wantOutcome)
			}
			if got.FilledQty()+got.Remaining != tc.taker.Qty {
				t.Errorf("filled %d + remaining %d != order qty %d", got.FilledQty(), got.Remaining, tc.taker.Qty)
			}
			d := b.Depth(0)
			if !slices.Equal(d.Bids, tc.wantBids) || !slices.Equal(d.Asks, tc.wantAsks) {
				t.Errorf("book after Place:\n%v", d)
			}
		})
	}
}

func TestPartialFillKeepsQueuePosition(t *testing.T) {
	b := New()
	mustAdd(t, b, mk(1, 1, domain.Sell, 100, 3000), mk(2, 2, domain.Sell, 100, 1000))

	if _, err := b.Place(mk(10, 9, domain.Buy, 100, 1000), domain.GTC); err != nil {
		t.Fatal(err)
	}
	mustBeValid(t, b)

	// Order 1 was partly filled, but it is still first in line: a partial
	// fill never costs a maker its time priority.
	if got := queueIDs(b, domain.Sell, 100); !slices.Equal(got, []domain.OrderID{1, 2}) {
		t.Errorf("queue = %v, want [1 2]", got)
	}
	if o, _ := b.Get(1); o.Qty != 2000 {
		t.Errorf("order 1 remaining = %d, want 2000", o.Qty)
	}
}

func TestFilledMakerLeavesTheBook(t *testing.T) {
	b := New()
	mustAdd(t, b, mk(1, 1, domain.Sell, 100, 1000))
	if _, err := b.Place(mk(10, 9, domain.Buy, 100, 1000), domain.IOC); err != nil {
		t.Fatal(err)
	}
	mustBeValid(t, b)

	if _, ok := b.Get(1); ok {
		t.Error("a fully filled maker is still in the book")
	}
	if _, err := b.Cancel(1); !errors.Is(err, ErrUnknownOrder) {
		t.Errorf("Cancel(filled order) error = %v, want ErrUnknownOrder", err)
	}
}

func TestCancelAfterPartialFillReturnsRemainder(t *testing.T) {
	b := New()
	mustAdd(t, b, mk(1, 1, domain.Sell, 100, 3000))
	if _, err := b.Place(mk(10, 9, domain.Buy, 100, 1000), domain.IOC); err != nil {
		t.Fatal(err)
	}

	got, err := b.Cancel(1)
	if err != nil {
		t.Fatal(err)
	}
	// Later, state releases a reservation for exactly this remainder, so it
	// must be what is LEFT (2000), not the original size (3000).
	if got.Qty != 2000 {
		t.Errorf("Cancel returned qty %d, want the unfilled 2000", got.Qty)
	}
	mustBeValid(t, b)
}

func TestFilledOrderStillConsumesItsSeq(t *testing.T) {
	b := New()
	mustAdd(t, b, mk(1, 1, domain.Sell, 100, 1000))
	if _, err := b.Place(mk(2, 9, domain.Buy, 100, 1000), domain.IOC); err != nil {
		t.Fatal(err)
	}
	// Order 2 never rested, but its Seq (2) was used. Another order with
	// Seq 2 must be rejected, or replay order would be ambiguous.
	o := mk(3, 9, domain.Buy, 90, 1000)
	o.Seq = 2
	if _, err := b.Place(o, domain.GTC); !errors.Is(err, ErrSeqNotIncreasing) {
		t.Errorf("Place with a reused Seq: error = %v, want ErrSeqNotIncreasing", err)
	}
}

func TestPlaceRejectsInvalidTimeInForce(t *testing.T) {
	b := New()
	mustAdd(t, b, mk(1, 1, domain.Sell, 100, 1000))
	before := b.Depth(0)

	_, err := b.Place(mk(10, 9, domain.Buy, 100, 1000), 0) // unset time in force
	if !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("error = %v, want ErrInvalidOrder", err)
	}
	if after := b.Depth(0); !slices.Equal(after.Asks, before.Asks) {
		t.Errorf("rejected order changed the book:\n%v", after)
	}
	mustBeValid(t, b)
}
