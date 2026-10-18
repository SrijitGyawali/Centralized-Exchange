package book

import (
	"fmt"
	"math"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// Book is the limit order book for one symbol: resting bids and asks,
// sorted by price, FIFO within each price.
//
//	Book
//	├── bids  ladder: levels sorted high → low
//	├── asks  ladder: levels sorted low → high
//	│         each level: FIFO doubly linked queue of orders
//	└── orders  map[OrderID]*node: jump straight to any order (for cancel)
//
// Cost summary (P = price levels on one side):
//
//	Add, existing price:  O(log P) find + O(1) append
//	Add, new price:       O(log P) find + O(P) insert
//	Cancel:               O(1) unlink, + O(P) only if the level empties
//	Best bid / ask:       O(1)
//
// A Book is NOT safe for concurrent use, on purpose. The engine's single
// owner goroutine (ADR-0001) is the only code that ever touches it, so it
// needs no mutex. Other goroutines receive copies (Depth, QueueAt, Get).
type Book struct {
	bids, asks ladder
	orders     map[domain.OrderID]*node // order-ID index
	lastSeq    domain.Seq               // highest Seq accepted so far
}

// New returns an empty book. A constructor is needed because the zero value
// of a map is nil, and writing to a nil map panics.
func New() *Book {
	return &Book{
		bids:   ladder{desc: true},
		asks:   ladder{desc: false},
		orders: make(map[domain.OrderID]*node),
	}
}

// Add rests order o without matching, rejecting it with ErrWouldCross if it
// would trade.
//
// Deprecated: use Place, which matches crossing orders. Add remains only
// while the tests migrate to Place, and is then removed.
func (b *Book) Add(o Order) error {
	if err := b.checkNew(o); err != nil {
		return err
	}
	if b.wouldCross(o) {
		return fmt.Errorf("%w: %v at %d", ErrWouldCross, o.Side, o.Price)
	}
	b.lastSeq = o.Seq
	b.rest(o)
	return nil
}

// checkNew runs every check a new order must pass before the book changes
// anything: valid fields, a fresh ID, an increasing Seq, and room in its
// level's total. If it returns nil, the order can be matched and rested
// without any further failure possible.
//
// This is the "validate everything, then mutate" rule: a rejected order
// leaves the book exactly as it was, so a command is never half-applied.
func (b *Book) checkNew(o Order) error {
	if err := o.validate(); err != nil {
		return err
	}
	if _, exists := b.orders[o.ID]; exists {
		return fmt.Errorf("%w: %d", ErrDuplicateOrder, o.ID)
	}
	if o.Seq <= b.lastSeq {
		return fmt.Errorf("%w: got %d, last was %d", ErrSeqNotIncreasing, o.Seq, b.lastSeq)
	}
	ld := b.sideOf(o.Side)
	if i, found := ld.search(o.Price); found && ld.levels[i].total > math.MaxInt64-o.Qty {
		// Unreachable within the configured bounds (it would take about 9e13
		// orders at one price), but checked anyway: Go integers wrap
		// silently on overflow, and a wrong total is wrong market data.
		return fmt.Errorf("total at price %d: %w", o.Price, domain.ErrOverflow)
	}
	return nil
}

// rest appends o to the back of the queue at its price, creating the level
// if needed. The caller must have run checkNew and confirmed o does not
// cross the opposite side.
func (b *Book) rest(o Order) {
	ld := b.sideOf(o.Side)
	i, found := ld.search(o.Price)
	var l *level
	if found {
		l = ld.levels[i]
	} else {
		l = ld.insertAt(i, o.Price)
	}
	n := &node{order: o}
	l.pushBack(n)
	b.orders[o.ID] = n
}

// wouldCross reports whether o would trade against the opposite side.
func (b *Book) wouldCross(o Order) bool {
	if o.Side == domain.Buy {
		ask := b.asks.best()
		return ask != nil && o.Price >= ask.price
	}
	bid := b.bids.best()
	return bid != nil && o.Price <= bid.price
}

// Cancel removes a resting order and returns it as it was just before
// removal. Its remaining Qty is what the state package will use to release
// the order's reserved funds.
//
// Cancelling an unknown ID, including one already cancelled, returns
// ErrUnknownOrder and changes nothing. A repeated cancel therefore can never
// release funds twice.
func (b *Book) Cancel(id domain.OrderID) (Order, error) {
	n, ok := b.orders[id]
	if !ok {
		return Order{}, fmt.Errorf("%w: %d", ErrUnknownOrder, id)
	}
	l := n.level
	l.remove(n) // O(1): the index handed us the node directly
	delete(b.orders, id)
	if l.empty() {
		b.sideOf(n.order.Side).removeLevel(l) // empty levels never linger
	}
	return n.order, nil
}

// Get returns a copy of the resting order with this ID.
func (b *Book) Get(id domain.OrderID) (Order, bool) {
	n, ok := b.orders[id]
	if !ok {
		return Order{}, false
	}
	return n.order, true
}

// QueueAt returns copies of the orders resting at one price, front of the
// queue first: the order in which they would trade. It returns nil if no
// orders rest there.
func (b *Book) QueueAt(side domain.Side, p domain.Price) []Order {
	ld := b.sideOf(side)
	i, found := ld.search(p)
	if !found {
		return nil
	}
	l := ld.levels[i]
	out := make([]Order, 0, l.orders)
	for n := l.head; n != nil; n = n.next {
		out = append(out, n.order)
	}
	return out
}

// BestBid returns the highest bid price, or false if there are no bids.
func (b *Book) BestBid() (domain.Price, bool) { return bestPrice(&b.bids) }

// BestAsk returns the lowest ask price, or false if there are no asks.
func (b *Book) BestAsk() (domain.Price, bool) { return bestPrice(&b.asks) }

func bestPrice(ld *ladder) (domain.Price, bool) {
	if l := ld.best(); l != nil {
		return l.price, true
	}
	return 0, false
}

// Len returns the number of resting orders.
func (b *Book) Len() int { return len(b.orders) }

// sideOf returns the ladder that orders of side s rest on. Callers have
// already validated s, so anything other than Buy means Sell.
func (b *Book) sideOf(s domain.Side) *ladder {
	if s == domain.Buy {
		return &b.bids
	}
	return &b.asks
}
