package book

import (
	"errors"
	"slices"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// --- helpers ---------------------------------------------------------------

// ord builds an order with short arguments so test tables stay readable.
// Seq = id keeps sequence numbers increasing when ids are used in order.
func ord(id uint64, side domain.Side, price, qty int64) Order {
	return Order{
		ID:    domain.OrderID(id),
		User:  1,
		Side:  side,
		Price: domain.Price(price),
		Qty:   domain.Qty(qty),
		Seq:   domain.Seq(id),
	}
}

// mustAdd adds o and verifies every invariant; it stops the test on error.
// t.Helper() makes failures point at the caller's line, not this one.
func mustAdd(t *testing.T, b *Book, orders ...Order) {
	t.Helper()
	for _, o := range orders {
		if err := b.Add(o); err != nil {
			t.Fatalf("Add(%+v): %v", o, err)
		}
		mustBeValid(t, b)
	}
}

func mustBeValid(t *testing.T, b *Book) {
	t.Helper()
	if err := b.CheckInvariants(); err != nil {
		t.Fatalf("invariant broken: %v", err)
	}
}

// queueIDs returns the order IDs at one price, front of the queue first.
func queueIDs(b *Book, side domain.Side, price int64) []domain.OrderID {
	var ids []domain.OrderID
	for _, o := range b.QueueAt(side, domain.Price(price)) {
		ids = append(ids, o.ID)
	}
	return ids
}

func prices(rows []LevelSummary) []domain.Price {
	var ps []domain.Price
	for _, r := range rows {
		ps = append(ps, r.Price)
	}
	return ps
}

// --- tests -----------------------------------------------------------------

func TestNewBookIsEmpty(t *testing.T) {
	b := New()
	mustBeValid(t, b)
	if b.Len() != 0 {
		t.Errorf("Len() = %d, want 0", b.Len())
	}
	if _, ok := b.BestBid(); ok {
		t.Error("BestBid() reported a price on an empty book")
	}
	if _, ok := b.BestAsk(); ok {
		t.Error("BestAsk() reported a price on an empty book")
	}
	if d := b.Depth(0); len(d.Bids)+len(d.Asks) != 0 {
		t.Errorf("Depth(0) = %+v, want empty", d)
	}
}

func TestLevelsAreSortedBestFirst(t *testing.T) {
	b := New()
	// Added in scrambled order on purpose.
	mustAdd(t, b,
		ord(1, domain.Buy, 100, 1), ord(2, domain.Buy, 102, 1), ord(3, domain.Buy, 101, 1),
		ord(4, domain.Sell, 110, 1), ord(5, domain.Sell, 108, 1), ord(6, domain.Sell, 109, 1),
	)

	d := b.Depth(0)
	if got, want := prices(d.Bids), []domain.Price{102, 101, 100}; !slices.Equal(got, want) {
		t.Errorf("bid prices = %v, want %v (highest first)", got, want)
	}
	if got, want := prices(d.Asks), []domain.Price{108, 109, 110}; !slices.Equal(got, want) {
		t.Errorf("ask prices = %v, want %v (lowest first)", got, want)
	}
	if bid, _ := b.BestBid(); bid != 102 {
		t.Errorf("BestBid() = %d, want 102", bid)
	}
	if ask, _ := b.BestAsk(); ask != 108 {
		t.Errorf("BestAsk() = %d, want 108", ask)
	}
}

func TestSamePriceQueuesFIFO(t *testing.T) {
	b := New()
	mustAdd(t, b,
		ord(1, domain.Buy, 100, 1_000),
		ord(2, domain.Buy, 100, 2_500),
		ord(3, domain.Buy, 100, 500),
	)

	if got, want := queueIDs(b, domain.Buy, 100), []domain.OrderID{1, 2, 3}; !slices.Equal(got, want) {
		t.Errorf("queue = %v, want %v (first in, first out)", got, want)
	}
	want := LevelSummary{Price: 100, Qty: 4_000, Orders: 3}
	if got := b.Depth(0).Bids[0]; got != want {
		t.Errorf("level = %+v, want %+v", got, want)
	}
}

func TestCancelFromAnyQueuePosition(t *testing.T) {
	tests := []struct {
		name      string
		cancel    domain.OrderID
		wantQueue []domain.OrderID
		wantQty   domain.Qty
	}{
		{"head", 1, []domain.OrderID{2, 3}, 3_000},
		{"middle", 2, []domain.OrderID{1, 3}, 1_500},
		{"tail", 3, []domain.OrderID{1, 2}, 3_500},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := New()
			mustAdd(t, b,
				ord(1, domain.Buy, 100, 1_000),
				ord(2, domain.Buy, 100, 2_500),
				ord(3, domain.Buy, 100, 500),
			)
			before, _ := b.Get(tc.cancel)

			got, err := b.Cancel(tc.cancel)
			if err != nil {
				t.Fatalf("Cancel(%d): %v", tc.cancel, err)
			}
			mustBeValid(t, b)

			if got != before {
				t.Errorf("Cancel returned %+v, want the resting order %+v", got, before)
			}
			if q := queueIDs(b, domain.Buy, 100); !slices.Equal(q, tc.wantQueue) {
				t.Errorf("queue after cancel = %v, want %v", q, tc.wantQueue)
			}
			if lvl := b.Depth(0).Bids[0]; lvl.Qty != tc.wantQty || lvl.Orders != 2 {
				t.Errorf("level after cancel = %+v, want qty %d with 2 orders", lvl, tc.wantQty)
			}
			if _, ok := b.Get(tc.cancel); ok {
				t.Error("cancelled order is still found by Get")
			}
		})
	}
}

func TestCancelLastOrderRemovesLevel(t *testing.T) {
	b := New()
	mustAdd(t, b, ord(1, domain.Buy, 101, 1), ord(2, domain.Buy, 100, 1))

	if _, err := b.Cancel(1); err != nil {
		t.Fatal(err)
	}
	mustBeValid(t, b)

	if got := prices(b.Depth(0).Bids); !slices.Equal(got, []domain.Price{100}) {
		t.Errorf("bid levels = %v, want [100]: the empty 101 level must be deleted", got)
	}
	if bid, _ := b.BestBid(); bid != 100 {
		t.Errorf("BestBid() = %d, want 100", bid)
	}
}

func TestCancelUnknownOrTwice(t *testing.T) {
	b := New()
	mustAdd(t, b, ord(1, domain.Sell, 100, 1))

	if _, err := b.Cancel(99); !errors.Is(err, ErrUnknownOrder) {
		t.Errorf("Cancel(unknown) error = %v, want ErrUnknownOrder", err)
	}
	if _, err := b.Cancel(1); err != nil {
		t.Fatalf("first Cancel(1): %v", err)
	}
	// The second cancel must fail. Later, the state package releases a
	// reservation per successful cancel, so this is what stops a double release.
	if _, err := b.Cancel(1); !errors.Is(err, ErrUnknownOrder) {
		t.Errorf("second Cancel(1) error = %v, want ErrUnknownOrder", err)
	}
	mustBeValid(t, b)
}

// TestAddRejects checks every rejection, and that a rejected Add leaves the
// book exactly as it was (no partial changes).
func TestAddRejects(t *testing.T) {
	base := func(t *testing.T) *Book {
		b := New()
		mustAdd(t, b, ord(1, domain.Buy, 100, 1_000), ord(2, domain.Sell, 110, 1_000))
		return b
	}
	valid := ord(3, domain.Buy, 105, 1_000)
	with := func(change func(*Order)) Order { o := valid; change(&o); return o }

	tests := []struct {
		name     string
		order    Order
		wantErrs []error // every one must match with errors.Is
	}{
		{"zero id", with(func(o *Order) { o.ID = 0 }), []error{ErrInvalidOrder}},
		{"zero user", with(func(o *Order) { o.User = 0 }), []error{ErrInvalidOrder}},
		{"unset side", with(func(o *Order) { o.Side = 0 }), []error{ErrInvalidOrder}},
		{"zero price", with(func(o *Order) { o.Price = 0 }), []error{ErrInvalidOrder, domain.ErrOutOfRange}},
		{"price above max", with(func(o *Order) { o.Price = domain.MaxPrice + 1 }), []error{ErrInvalidOrder, domain.ErrOutOfRange}},
		{"zero qty", with(func(o *Order) { o.Qty = 0 }), []error{ErrInvalidOrder, domain.ErrOutOfRange}},
		{"negative qty", with(func(o *Order) { o.Qty = -5 }), []error{ErrInvalidOrder, domain.ErrOutOfRange}},
		{"qty above max", with(func(o *Order) { o.Qty = domain.MaxQty + 1 }), []error{ErrInvalidOrder, domain.ErrOutOfRange}},
		{"duplicate id", with(func(o *Order) { o.ID = 1 }), []error{ErrDuplicateOrder}},
		{"seq equal to last", with(func(o *Order) { o.Seq = 2 }), []error{ErrSeqNotIncreasing}},
		{"seq below last", with(func(o *Order) { o.Seq = 1 }), []error{ErrSeqNotIncreasing}},
		{"buy at best ask", with(func(o *Order) { o.Price = 110 }), []error{ErrWouldCross}},
		{"buy above best ask", with(func(o *Order) { o.Price = 120 }), []error{ErrWouldCross}},
		{"sell at best bid", with(func(o *Order) { o.Side, o.Price = domain.Sell, 100 }), []error{ErrWouldCross}},
		{"sell below best bid", with(func(o *Order) { o.Side, o.Price = domain.Sell, 90 }), []error{ErrWouldCross}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := base(t)
			before, beforeLen := b.Depth(0), b.Len()

			err := b.Add(tc.order) // must not panic, whatever the input
			for _, want := range tc.wantErrs {
				if !errors.Is(err, want) {
					t.Errorf("Add(%+v) error = %v, want it to match %v", tc.order, err, want)
				}
			}

			mustBeValid(t, b)
			after := b.Depth(0)
			if b.Len() != beforeLen || !slices.Equal(after.Bids, before.Bids) || !slices.Equal(after.Asks, before.Asks) {
				t.Errorf("rejected Add changed the book:\nbefore:\n%vafter:\n%v", before, after)
			}
		})
	}

	// The same order with nothing wrong is accepted: proves the table's base
	// order is valid, so each row really tests only its own change.
	b := base(t)
	mustAdd(t, b, valid)
}

func TestSnapshotsAreCopies(t *testing.T) {
	b := New()
	mustAdd(t, b, ord(1, domain.Buy, 100, 1_000), ord(2, domain.Buy, 100, 2_000), ord(3, domain.Sell, 110, 500))

	d := b.Depth(0)
	d.Bids[0].Qty = 999_999 // scribble on the snapshot...
	d.Asks = append(d.Asks, LevelSummary{Price: 1})

	q := b.QueueAt(domain.Buy, 100)
	q[0].Qty = 1

	got, _ := b.Get(1)
	got.Price = 5

	// ...and the book must not notice any of it.
	mustBeValid(t, b)
	fresh := b.Depth(0)
	if fresh.Bids[0].Qty != 3_000 || len(fresh.Asks) != 1 {
		t.Errorf("book changed through a Depth snapshot: %+v", fresh)
	}
	if o, _ := b.Get(1); o.Qty != 1_000 || o.Price != 100 {
		t.Errorf("book changed through QueueAt/Get copies: %+v", o)
	}
}

func TestDepthLimit(t *testing.T) {
	b := New()
	mustAdd(t, b,
		ord(1, domain.Buy, 100, 1), ord(2, domain.Buy, 99, 1), ord(3, domain.Buy, 98, 1),
		ord(4, domain.Sell, 101, 1), ord(5, domain.Sell, 102, 1),
	)
	tests := []struct {
		n                  int
		wantBids, wantAsks int
	}{
		{0, 3, 2},   // n <= 0: all levels
		{-1, 3, 2},  // n <= 0: all levels
		{1, 1, 1},   // top of book only
		{2, 2, 2},   // asks have only 2
		{100, 3, 2}, // more than exist: all levels
	}
	for _, tc := range tests {
		d := b.Depth(tc.n)
		if len(d.Bids) != tc.wantBids || len(d.Asks) != tc.wantAsks {
			t.Errorf("Depth(%d) has %d bids / %d asks, want %d / %d", tc.n, len(d.Bids), len(d.Asks), tc.wantBids, tc.wantAsks)
		}
	}
	if top := b.Depth(1); top.Bids[0].Price != 100 || top.Asks[0].Price != 101 {
		t.Errorf("Depth(1) = %+v, want the best bid 100 and best ask 101", top)
	}
}
