package book

import (
	"cmp"
	"errors"
	"maps"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// refBook is a deliberately slow, obviously-correct model of an order book:
// a plain map of live orders. Every question is answered by scanning all of
// them. If the real Book (linked lists, sorted slices, an index) ever
// disagrees with this model, the real Book has a bug.
type refBook struct {
	live map[domain.OrderID]Order
	// ids lists live orders in insertion order. Tests pick random orders from
	// this slice, not from the map, because Go deliberately randomizes map
	// iteration order. With a slice, a given seed always produces the same
	// run, so any failure can be reproduced exactly.
	ids []domain.OrderID
}

func (r *refBook) add(o Order) {
	r.live[o.ID] = o
	r.ids = append(r.ids, o.ID)
}

func (r *refBook) cancel(id domain.OrderID) {
	delete(r.live, id)
	r.ids = slices.DeleteFunc(r.ids, func(x domain.OrderID) bool { return x == id })
}

// crosses: a buy crosses if any ask is at or below its price; a sell crosses
// if any bid is at or above its price.
func (r *refBook) crosses(o Order) bool {
	for _, x := range r.live {
		if x.Side == o.Side {
			continue
		}
		if (o.Side == domain.Buy && x.Price <= o.Price) || (o.Side == domain.Sell && x.Price >= o.Price) {
			return true
		}
	}
	return false
}

func (r *refBook) depth() Depth {
	agg := map[domain.Side]map[domain.Price]LevelSummary{domain.Buy: {}, domain.Sell: {}}
	for _, o := range r.live {
		s := agg[o.Side][o.Price]
		s.Price = o.Price
		s.Qty += o.Qty
		s.Orders++
		agg[o.Side][o.Price] = s
	}
	bids := slices.Collect(maps.Values(agg[domain.Buy]))
	asks := slices.Collect(maps.Values(agg[domain.Sell]))
	slices.SortFunc(bids, func(a, b LevelSummary) int { return cmp.Compare(b.Price, a.Price) }) // high → low
	slices.SortFunc(asks, func(a, b LevelSummary) int { return cmp.Compare(a.Price, b.Price) }) // low → high
	return Depth{Bids: bids, Asks: asks}
}

// queue returns the live orders at one price, sorted by Seq: FIFO by definition.
func (r *refBook) queue(side domain.Side, p domain.Price) []Order {
	var q []Order
	for _, o := range r.live {
		if o.Side == side && o.Price == p {
			q = append(q, o)
		}
	}
	slices.SortFunc(q, func(a, b Order) int { return cmp.Compare(a.Seq, b.Seq) })
	return q
}

// TestRandomOperationsMatchReference runs thousands of random adds and
// cancels against both the real Book and refBook, and demands that they agree
// on every result, every queue and every depth snapshot. It checks all
// invariants after every single step.
//
// The seed is fixed, so the "random" sequence is identical on every run and
// on every machine. Change the seed to explore new sequences.
func TestRandomOperationsMatchReference(t *testing.T) {
	const steps = 5_000
	rng := rand.New(rand.NewPCG(2026, 10))

	b := New()
	ref := &refBook{live: map[domain.OrderID]Order{}}
	nextID := uint64(1)
	var adds, crossRejects, cancels, unknownCancels int

	for step := 0; step < steps; step++ {
		if len(ref.ids) == 0 || rng.IntN(100) < 65 {
			// Add a random order. Bids land on 950..1009 and asks on 1000..1059.
			// The overlap makes some orders cross, exercising ErrWouldCross.
			o := Order{
				ID:   domain.OrderID(nextID),
				User: domain.UserID(1 + rng.IntN(20)),
				Qty:  domain.Qty(1 + rng.Int64N(5_000)),
				Seq:  domain.Seq(nextID),
			}
			nextID++
			if rng.IntN(2) == 0 {
				o.Side, o.Price = domain.Buy, domain.Price(950+rng.Int64N(60))
			} else {
				o.Side, o.Price = domain.Sell, domain.Price(1000+rng.Int64N(60))
			}

			wantCross := ref.crosses(o)
			err := b.Add(o)
			switch {
			case wantCross && !errors.Is(err, ErrWouldCross):
				t.Fatalf("step %d: Add(%+v) = %v, but the reference says it crosses", step, o, err)
			case !wantCross && err != nil:
				t.Fatalf("step %d: Add(%+v) = %v, but the reference accepts it", step, o, err)
			case err == nil:
				ref.add(o)
				adds++
			default:
				crossRejects++
			}
		} else {
			// Cancel: usually a live order, sometimes an ID that never existed.
			var id domain.OrderID
			if rng.IntN(10) == 0 {
				id = domain.OrderID(1_000_000 + nextID) // never issued
			} else {
				id = ref.ids[rng.IntN(len(ref.ids))]
			}

			got, err := b.Cancel(id)
			want, known := ref.live[id]
			switch {
			case known && (err != nil || got != want):
				t.Fatalf("step %d: Cancel(%d) = %+v, %v; want %+v", step, id, got, err, want)
			case !known && !errors.Is(err, ErrUnknownOrder):
				t.Fatalf("step %d: Cancel(unknown %d) error = %v, want ErrUnknownOrder", step, id, err)
			case known:
				ref.cancel(id)
				cancels++
			default:
				unknownCancels++
			}
		}

		if err := b.CheckInvariants(); err != nil {
			t.Fatalf("step %d: %v", step, err)
		}
		if b.Len() != len(ref.live) {
			t.Fatalf("step %d: Len() = %d, reference has %d", step, b.Len(), len(ref.live))
		}
		if step%25 == 0 || step == steps-1 {
			compareWithReference(t, step, b, ref)
		}
	}

	t.Logf("%d steps: %d adds, %d crossing rejects, %d cancels, %d unknown cancels; %d orders resting at the end",
		steps, adds, crossRejects, cancels, unknownCancels, b.Len())
	if adds == 0 || crossRejects == 0 || cancels == 0 || unknownCancels == 0 {
		t.Error("the random mix did not exercise every path; adjust the probabilities")
	}
}

// compareWithReference checks the full depth and every queue, in FIFO order.
func compareWithReference(t *testing.T, step int, b *Book, ref *refBook) {
	t.Helper()
	got, want := b.Depth(0), ref.depth()
	if !slices.Equal(got.Bids, want.Bids) || !slices.Equal(got.Asks, want.Asks) {
		t.Fatalf("step %d: depth differs\n--- book:\n%v--- reference:\n%v", step, got, want)
	}
	for side, rows := range map[domain.Side][]LevelSummary{domain.Buy: want.Bids, domain.Sell: want.Asks} {
		for _, row := range rows {
			if q, wq := b.QueueAt(side, row.Price), ref.queue(side, row.Price); !slices.Equal(q, wq) {
				t.Fatalf("step %d: %v queue at %d differs\nbook: %+v\nref:  %+v", step, side, row.Price, q, wq)
			}
		}
	}
}
