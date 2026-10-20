package engine

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// These tests are meant for the race detector (`go test -race`, which
// `make check` and CI always use). It watches every memory access while the
// tests run and fails if two goroutines touch the same memory without
// synchronisation. It can only see paths that actually execute, which is why
// these tests push many goroutines through every engine entry point at once.

// TestConcurrentSubmissionsAndReads: eight users trade at the same time
// while four readers poll snapshots and balances. Afterwards:
//   - every command got a unique Seq, with no gaps (one owner, one sequence);
//   - all money and order invariants hold;
//   - each asset's balances still add up to what was funded;
//   - readers never saw time go backwards.
func TestConcurrentSubmissionsAndReads(t *testing.T) {
	e := started(t, DefaultConfig())
	users := []domain.UserID{1, 2, 3, 4, 5, 6, 7, 8}
	for _, u := range users {
		mustSubmit(t, e, operator, newKey(), command.Fund{To: u, Asset: domain.USDT, Amount: usdt(1_000_000)})
		mustSubmit(t, e, operator, newKey(), command.Fund{To: u, Asset: domain.BTC, Amount: 100 * domain.LotsPerBTC})
	}
	fundings := domain.Seq(2 * len(users))

	const perUser = 250
	seqs := make(chan domain.Seq, len(users)*perUser)
	var writers sync.WaitGroup
	for _, u := range users {
		writers.Go(func() {
			rng := rand.New(rand.NewPCG(uint64(u), 7)) // per-goroutine: rand.Rand is not safe to share
			var resting []domain.OrderID
			for i := range perUser {
				var body command.Body
				if len(resting) > 0 && rng.IntN(5) == 0 {
					body = command.CancelOrder{Order: resting[len(resting)-1]} // may have filled meanwhile: fine
					resting = resting[:len(resting)-1]
				} else {
					p := command.PlaceOrder{
						Side: domain.Buy, Price: domain.Price(59_995 + rng.Int64N(11)),
						Qty: domain.Qty(1 + rng.Int64N(2_000)), TIF: domain.GTC,
					}
					if rng.IntN(2) == 0 {
						p.Side = domain.Sell
					}
					if rng.IntN(4) == 0 {
						p.TIF = domain.IOC
					}
					body = p
				}
				res, err := e.Submit(t.Context(), u, fmt.Sprintf("u%d-%d", u, i), body)
				if err != nil {
					t.Errorf("user %d: %v", u, err)
					return
				}
				seqs <- res.Seq
				if res.Accepted() && res.Outcome == book.Rested {
					resting = append(resting, res.Order)
				}
			}
		})
	}

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for r := range 4 {
		readers.Go(func() {
			var lastSnap, lastAsOf domain.Seq
			for {
				select {
				case <-stop:
					return
				default:
				}
				s := e.Snapshot() // lock-free read of an immutable value
				if s.Seq < lastSnap {
					t.Errorf("reader %d: snapshot went back from seq %d to %d", r, lastSnap, s.Seq)
					return
				}
				lastSnap = s.Seq
				if len(s.Book.Bids) > 0 && len(s.Book.Asks) > 0 && s.Book.Bids[0].Price >= s.Book.Asks[0].Price {
					t.Errorf("reader %d: snapshot shows a crossed book: %+v", r, s.Book)
					return
				}
				bal, err := e.Balance(t.Context(), users[r], domain.USDT) // through the owner, in order
				if err != nil {
					t.Errorf("reader %d: %v", r, err)
					return
				}
				if bal.AsOf < lastAsOf {
					t.Errorf("reader %d: balance went back from seq %d to %d", r, lastAsOf, bal.AsOf)
					return
				}
				lastAsOf = bal.AsOf
			}
		})
	}

	writers.Wait()
	close(stop)
	readers.Wait()
	close(seqs)

	got := slices.Sorted(func(yield func(domain.Seq) bool) {
		for s := range seqs {
			if !yield(s) {
				return
			}
		}
	})
	if len(got) != len(users)*perUser {
		t.Fatalf("%d commands completed, want %d", len(got), len(users)*perUser)
	}
	for i, s := range got {
		if want := fundings + domain.Seq(i) + 1; s != want {
			t.Fatalf("sorted Seqs[%d] = %d, want %d: Seqs must be unique and gap-free", i, s, want)
		}
	}

	type report struct {
		invariants   error
		held, issued [3]int64 // indexed by asset
	}
	rep, err := Query(t.Context(), e, func(s StateReader) report {
		var r report
		r.invariants = s.CheckInvariants()
		for _, a := range []domain.Asset{domain.BTC, domain.USDT} {
			r.issued[a] = s.Issued(a)
			for _, u := range users {
				avail, reserved := s.Balance(u, a)
				r.held[a] += avail + reserved
			}
		}
		return r
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.invariants != nil {
		t.Fatalf("invariants broken after concurrent trading: %v", rep.invariants)
	}
	if rep.held != rep.issued {
		t.Errorf("users hold %v but %v was issued (BTC, USDT): money was created or lost", rep.held[1:], rep.issued[1:])
	}
}

// TestSameKeyConcurrentlyHasOneEffect: 100 goroutines send the IDENTICAL
// request with the SAME key at the same instant, which is what a client
// retrying aggressively after a timeout can look like. Exactly one may
// execute; the other 99 must get that one's result back as duplicates.
func TestSameKeyConcurrentlyHasOneEffect(t *testing.T) {
	e := started(t, DefaultConfig())
	mustSubmit(t, e, operator, newKey(), fundAlice(usdt(1_000_000)))

	const n = 100
	body := buy(60_000, 1_000, domain.GTC) // reserves 6,000 USDT
	results := make([]command.Result, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start // every goroutine blocks here...
			res, err := e.Submit(t.Context(), alice, "same-key", body)
			if err != nil {
				t.Error(err)
				return
			}
			results[i] = res
		})
	}
	close(start) // ...and closing the channel releases all of them at once
	wg.Wait()

	originals := 0
	for _, r := range results {
		if !r.Duplicate {
			originals++
		}
		if r.Order != results[0].Order || r.Seq != results[0].Seq || !r.Accepted() {
			t.Fatalf("results disagree: %+v vs %+v; every caller must see the one original result", r, results[0])
		}
	}
	if originals != 1 {
		t.Fatalf("%d requests executed, want exactly 1", originals)
	}

	bal, err := e.Balance(t.Context(), alice, domain.USDT)
	if err != nil {
		t.Fatal(err)
	}
	if bal.Reserved != usdt(6_000) {
		t.Errorf("Alice has %d reserved, want 6,000 USDT: one order, not %d", bal.Reserved, n)
	}
	if bids := e.Snapshot().Book.Bids; len(bids) != 1 || bids[0].Orders != 1 {
		t.Errorf("book bids = %+v, want exactly one order", bids)
	}
}

// TestSameKeyDifferentRequestsConcurrently: 50 goroutines race to use ONE
// key for 50 DIFFERENT orders. Whichever the engine sequences first owns the
// key; every other request is a key conflict and does nothing.
func TestSameKeyDifferentRequestsConcurrently(t *testing.T) {
	e := started(t, DefaultConfig())
	mustSubmit(t, e, operator, newKey(), fundAlice(usdt(1_000_000)))

	const n = 50
	results := make([]command.Result, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			res, err := e.Submit(t.Context(), alice, "contested", buy(60_000, int64(i+1), domain.GTC))
			if err != nil {
				t.Error(err)
				return
			}
			results[i] = res
		})
	}
	close(start)
	wg.Wait()

	winner, conflicts := -1, 0
	for i, r := range results {
		switch {
		case r.Accepted() && !r.Duplicate:
			if winner != -1 {
				t.Fatalf("requests %d and %d both executed under one key", winner, i)
			}
			winner = i
		case r.Rejected == command.RejectKeyConflict:
			conflicts++
		default:
			t.Fatalf("request %d: unexpected result %+v", i, r)
		}
	}
	if winner == -1 || conflicts != n-1 {
		t.Fatalf("winner %d, conflicts %d; want exactly one winner and %d conflicts", winner, conflicts, n-1)
	}
	want := []book.LevelSummary{{Price: 60_000, Qty: domain.Qty(winner + 1), Orders: 1}}
	if bids := e.Snapshot().Book.Bids; !slices.Equal(bids, want) {
		t.Errorf("book bids = %+v, want only the winner's order %+v", bids, want)
	}
}
