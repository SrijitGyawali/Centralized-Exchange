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

// refBook is a deliberately slow, obviously correct order book and matcher:
// a plain map of live orders, where every question ("who is the best maker?")
// is answered by scanning all of them. It shares no matching code with Book.
// If the real Book (sorted slices, linked lists, an index) ever disagrees
// with this model, the real Book has a bug.
type refBook struct {
	live map[domain.OrderID]Order
	// ids lists live orders in insertion order. Tests pick random orders from
	// this slice, never by ranging over the map, because Go deliberately
	// randomizes map iteration order. With a slice, a given seed always
	// produces the same run, so any failure can be reproduced exactly.
	ids []domain.OrderID
}

func (r *refBook) add(o Order) {
	r.live[o.ID] = o
	r.ids = append(r.ids, o.ID)
}

func (r *refBook) remove(id domain.OrderID) {
	delete(r.live, id)
	r.ids = slices.DeleteFunc(r.ids, func(x domain.OrderID) bool { return x == id })
}

// bestMaker scans every live order on side s for the one that should trade
// next: best price first (lowest ask / highest bid), then lowest Seq.
func (r *refBook) bestMaker(s domain.Side) (Order, bool) {
	var best Order
	found := false
	for _, id := range r.ids {
		o := r.live[id]
		if o.Side != s {
			continue
		}
		better := !found ||
			(s == domain.Sell && o.Price < best.Price) ||
			(s == domain.Buy && o.Price > best.Price) ||
			(o.Price == best.Price && o.Seq < best.Seq)
		if better {
			best, found = o, true
		}
	}
	return best, found
}

// place applies the Tier 1 matching rules in the most literal way possible.
func (r *refBook) place(o Order, tif domain.TimeInForce) Result {
	res := Result{Remaining: o.Qty}
	makerSide := domain.Sell
	if o.Side == domain.Sell {
		makerSide = domain.Buy
	}
	for res.Remaining > 0 {
		m, ok := r.bestMaker(makerSide)
		if !ok {
			break
		}
		if (o.Side == domain.Buy && m.Price > o.Price) || (o.Side == domain.Sell && m.Price < o.Price) {
			break // no longer crosses
		}
		if m.User == o.User {
			res.Outcome = CanceledSelfTrade
			break
		}
		q := min(res.Remaining, m.Qty)
		m.Qty -= q
		res.Remaining -= q
		res.Fills = append(res.Fills, Trade{
			Price: m.Price, Qty: q,
			Maker: m.ID, MakerUser: m.User, MakerRemaining: m.Qty,
			Taker: o.ID, TakerUser: o.User, TakerSide: o.Side,
		})
		if m.Qty == 0 {
			r.remove(m.ID)
		} else {
			r.live[m.ID] = m
		}
	}
	switch {
	case res.Outcome == CanceledSelfTrade:
	case res.Remaining == 0:
		res.Outcome = Filled
	case tif == domain.IOC:
		res.Outcome = CanceledIOC
	default:
		o.Qty = res.Remaining
		r.add(o)
		res.Outcome = Rested
	}
	return res
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

// TestRandomOperationsMatchReference runs thousands of random orders and
// cancels through both the real Book and refBook, and demands that they
// agree on every trade, every outcome, every queue and every depth snapshot.
// It also checks all invariants after every single step.
//
// Bids are drawn from 990..1003 and asks from 997..1010. The overlap makes
// many orders cross, and the offset lets a deep book build up, so orders
// often sweep several price levels. Only four users exist, so self-trade
// prevention happens often. The seed is fixed: the "random" sequence is
// identical on every run and every machine. Change the seed to explore.
func TestRandomOperationsMatchReference(t *testing.T) {
	const steps = 5_000
	rng := rand.New(rand.NewPCG(2026, 10))

	b := New()
	ref := &refBook{live: map[domain.OrderID]Order{}}
	nextSeq := uint64(1)
	count := map[string]int{}

	for step := 0; step < steps; step++ {
		if len(ref.ids) == 0 || rng.IntN(100) < 75 {
			o := Order{
				ID:    domain.OrderID(nextSeq),
				User:  domain.UserID(1 + rng.IntN(4)),
				Side:  domain.Buy,
				Price: domain.Price(990 + rng.Int64N(14)),
				Qty:   domain.Qty(1 + rng.Int64N(3_000)),
				Seq:   domain.Seq(nextSeq),
			}
			nextSeq++
			if rng.IntN(2) == 0 {
				o.Side, o.Price = domain.Sell, domain.Price(997+rng.Int64N(14))
			}
			tif := domain.GTC
			if rng.IntN(10) < 3 {
				tif = domain.IOC
			}

			want := ref.place(o, tif)
			got, err := b.Place(o, tif)
			if err != nil {
				t.Fatalf("step %d: Place(%+v, %v): %v", step, o, tif, err)
			}
			if !slices.Equal(got.Fills, want.Fills) || got.Remaining != want.Remaining || got.Outcome != want.Outcome {
				t.Fatalf("step %d: Place(%+v, %v)\n got  %+v\n want %+v", step, o, tif, got, want)
			}
			count["orders"]++
			count["fills"] += len(got.Fills)
			count[got.Outcome.String()]++
			if len(got.Fills) > 1 && got.Fills[0].Price != got.Fills[len(got.Fills)-1].Price {
				count["multi-level sweeps"]++
			}
		} else {
			// Cancel: usually a live order, sometimes an ID that never existed.
			var id domain.OrderID
			if rng.IntN(10) == 0 {
				id = domain.OrderID(1_000_000 + nextSeq) // never issued
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
				ref.remove(id)
				count["cancels"]++
			default:
				count["unknown cancels"]++
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

	t.Logf("%d steps, %d resting at the end: %v", steps, b.Len(), count)
	for _, path := range []string{
		"fills", "multi-level sweeps", Filled.String(), Rested.String(),
		CanceledIOC.String(), CanceledSelfTrade.String(), "cancels", "unknown cancels",
	} {
		if count[path] == 0 {
			t.Errorf("the random mix never exercised %q; adjust the probabilities", path)
		}
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
