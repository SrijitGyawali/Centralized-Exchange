package book

import (
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// TestCheckInvariantsCatchesCorruption tests the tester. Every other test
// trusts CheckInvariants to notice a broken book, and a checker that never
// fails proves nothing. So each case below corrupts a healthy book's
// internals in one specific way, which only code inside this package can
// do, and demands that CheckInvariants reports it.
func TestCheckInvariantsCatchesCorruption(t *testing.T) {
	healthy := func(t *testing.T) *Book {
		b := New()
		mustAdd(t, b,
			ord(1, domain.Buy, 100, 1_000), ord(2, domain.Buy, 100, 2_000), ord(3, domain.Buy, 99, 500),
			ord(4, domain.Sell, 110, 700), ord(5, domain.Sell, 111, 300),
		)
		return b
	}

	tests := []struct {
		name    string
		corrupt func(b *Book)
		wantMsg string // a phrase the error must contain
	}{
		{"levels out of order", func(b *Book) {
			b.bids.levels[0], b.bids.levels[1] = b.bids.levels[1], b.bids.levels[0]
		}, "out of order"},
		{"empty level left behind", func(b *Book) {
			b.asks.levels = append(b.asks.levels, &level{price: 120})
		}, "is empty"},
		{"broken prev link", func(b *Book) {
			b.bids.levels[0].head.next.prev = nil
		}, "broken prev link"},
		{"tail not last", func(b *Book) {
			l := b.bids.levels[0]
			l.tail = l.head
		}, "tail is not the last"},
		{"FIFO order broken", func(b *Book) {
			h := b.bids.levels[0].head
			h.order.Seq, h.next.order.Seq = h.next.order.Seq, h.order.Seq
		}, "FIFO broken"},
		{"cached total wrong", func(b *Book) {
			b.bids.levels[0].total++
		}, "cached"},
		{"order missing from index", func(b *Book) {
			delete(b.orders, 2)
		}, "missing from the index"},
		{"extra index entry", func(b *Book) {
			b.orders[99] = &node{order: ord(99, domain.Buy, 100, 1)}
		}, "index has"},
		{"book crossed", func(b *Book) {
			l := b.asks.levels[0] // move the best ask (110) down to 100, consistently
			l.price = 100
			l.head.order.Price = 100
		}, "crossed"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := healthy(t)
			tc.corrupt(b)
			err := b.CheckInvariants()
			if err == nil {
				t.Fatal("CheckInvariants() = nil on a corrupted book")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("CheckInvariants() = %q, want it to mention %q", err, tc.wantMsg)
			}
		})
	}
}
