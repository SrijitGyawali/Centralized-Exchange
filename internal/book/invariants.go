package book

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// CheckInvariants walks the entire book and returns an error describing the
// first broken rule, or nil if the book is consistent.
//
// It is O(number of orders), so it is meant for tests, fuzzing and the
// replay tool, never the hot path. The tests call it after every single
// Add and Cancel, so any bug that corrupts the structure is caught at the
// exact operation that caused it.
//
// The rules:
//  1. Each side's levels are strictly sorted best-first (no duplicate prices).
//  2. No level is empty (empty levels are deleted).
//  3. Each queue is correctly linked (prev/next agree, tail is the last node).
//  4. Every order in a level has that level's price and side.
//  5. Seq strictly increases from head to tail: FIFO really is time order.
//  6. Each level's cached orders/total match its queue.
//  7. The order-ID index contains exactly the orders in the queues.
//  8. No order's Seq is above the book's lastSeq.
//  9. The book is not crossed: best bid < best ask.
//  10. Every resting order has quantity left. A maker filled down to zero
//     must leave the book; a zero-quantity "ghost" would be matched again.
func (b *Book) CheckInvariants() error {
	indexed := 0
	sides := []struct {
		side domain.Side
		ld   *ladder
	}{{domain.Buy, &b.bids}, {domain.Sell, &b.asks}}

	for _, s := range sides {
		for i, l := range s.ld.levels {
			if i > 0 { // rule 1
				prev := s.ld.levels[i-1].price
				if (s.ld.desc && l.price >= prev) || (!s.ld.desc && l.price <= prev) {
					return fmt.Errorf("%v levels out of order at index %d: %d after %d", s.side, i, l.price, prev)
				}
			}
			if l.empty() || l.head == nil { // rule 2
				return fmt.Errorf("%v level %d is empty", s.side, l.price)
			}

			var (
				count   int
				total   domain.Qty
				prevSeq domain.Seq
				prev    *node
			)
			for n := l.head; n != nil; n = n.next {
				o := n.order
				switch {
				case count > len(b.orders):
					return fmt.Errorf("%v level %d: queue longer than the whole index (cycle?)", s.side, l.price)
				case n.prev != prev: // rule 3
					return fmt.Errorf("%v level %d: order %d has a broken prev link", s.side, l.price, o.ID)
				case n.level != l:
					return fmt.Errorf("order %d points to the wrong level", o.ID)
				case o.Price != l.price || o.Side != s.side: // rule 4
					return fmt.Errorf("order %d (%v %d) is in the %v %d level", o.ID, o.Side, o.Price, s.side, l.price)
				case o.Seq <= prevSeq: // rule 5
					return fmt.Errorf("%v level %d: FIFO broken, seq %d after %d", s.side, l.price, o.Seq, prevSeq)
				case o.Seq > b.lastSeq: // rule 8
					return fmt.Errorf("order %d has seq %d above lastSeq %d", o.ID, o.Seq, b.lastSeq)
				case o.Qty < domain.MinQty: // rule 10
					return fmt.Errorf("order %d rests with no quantity left (%d)", o.ID, o.Qty)
				case b.orders[o.ID] != n: // rule 7
					return fmt.Errorf("order %d is queued but missing from the index", o.ID)
				}
				count++
				total += o.Qty
				prevSeq = o.Seq
				prev = n
			}
			if l.tail != prev { // rule 3
				return fmt.Errorf("%v level %d: tail is not the last order", s.side, l.price)
			}
			if count != l.orders || total != l.total { // rule 6
				return fmt.Errorf("%v level %d: cached %d orders / %d lots, actual %d / %d",
					s.side, l.price, l.orders, l.total, count, total)
			}
			indexed += count
		}
	}

	if indexed != len(b.orders) { // rule 7
		return fmt.Errorf("index has %d orders but queues hold %d", len(b.orders), indexed)
	}
	if bid, ask := b.bids.best(), b.asks.best(); bid != nil && ask != nil && bid.price >= ask.price { // rule 9
		return fmt.Errorf("book is crossed: best bid %d >= best ask %d", bid.price, ask.price)
	}
	return nil
}
