package book

import "github.com/SrijitGyawali/Centralized-Exchange/internal/domain"

// node is one resting order inside a price level's queue.
//
// The queue is an *intrusive doubly linked list*: each node points to the
// order before it, the order after it, and its level. Holding a *node, which
// the book's order-ID index gives us, is enough to remove it in O(1), without
// searching the queue.
//
// The standard library's container/list would also work, but it stores
// values as `any`, so every access would need a type assertion. A few lines
// of our own keep the types exact and make the pointer work visible.
type node struct {
	order      Order
	prev, next *node
	level      *level
}

// level is every resting order at one price on one side, in FIFO order:
// head is the oldest order (it trades first), tail is the newest.
//
//	head                              tail
//	 ┌──────┐   ┌──────┐   ┌──────┐
//	 │ #1   │ ⇄ │ #2   │ ⇄ │ #3   │   all at 60,000
//	 └──────┘   └──────┘   └──────┘
//
// orders and total are kept in step with the list on every push/remove, so
// depth snapshots can read them in O(1) instead of walking the queue.
type level struct {
	price      domain.Price
	head, tail *node
	orders     int        // number of orders in the queue
	total      domain.Qty // sum of their remaining quantities
}

// pushBack appends n as the newest order (end of the queue). O(1).
// The caller must already have checked that total cannot overflow.
func (l *level) pushBack(n *node) {
	n.level = l
	n.prev = l.tail
	n.next = nil
	if l.tail != nil {
		l.tail.next = n
	} else {
		l.head = n // queue was empty: n is also the first
	}
	l.tail = n
	l.orders++
	l.total += n.order.Qty
}

// remove unlinks n from anywhere in the queue (head, middle or tail). O(1).
//
// Removing #2 from #1 ⇄ #2 ⇄ #3 just re-points #1.next to #3 and
// #3.prev to #1. Nothing else moves.
func (l *level) remove(n *node) {
	if n.prev != nil {
		n.prev.next = n.next
	} else {
		l.head = n.next // n was the head
	}
	if n.next != nil {
		n.next.prev = n.prev
	} else {
		l.tail = n.prev // n was the tail
	}
	n.prev, n.next, n.level = nil, nil, nil // no dangling pointers back into the book
	l.orders--
	l.total -= n.order.Qty
}

// empty reports whether the queue has no orders left.
func (l *level) empty() bool { return l.orders == 0 }
