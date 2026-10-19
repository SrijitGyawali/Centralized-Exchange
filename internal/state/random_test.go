package state

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

type userAsset struct {
	user  domain.UserID
	asset domain.Asset
}

// assertReplayIsIdentical feeds every command the harness applied into a
// brand-new State and demands identical effects (results AND journals) for
// each one, and an identical final state.
//
// This is determinism, the property crash recovery (week 4) rests on:
// after a crash the exchange rebuilds itself by replaying its command log,
// and that only works if replay reproduces the original exactly.
func assertReplayIsIdentical(t *testing.T, h *harness) {
	t.Helper()
	fresh := New()
	for i, cmd := range h.cmds {
		fx, err := fresh.Apply(cmd)
		if err != nil {
			t.Fatalf("replay of seq %d: %v", cmd.Seq, err)
		}
		if !reflect.DeepEqual(fx, h.effects[i]) {
			t.Fatalf("replay of seq %d produced different effects:\n got  %+v\n want %+v", cmd.Seq, fx, h.effects[i])
		}
	}
	switch {
	case !maps.Equal(fresh.balances, h.s.balances):
		t.Fatal("replayed balances differ")
	case !maps.Equal(fresh.open, h.s.open):
		t.Fatal("replayed open orders differ")
	case !reflect.DeepEqual(fresh.Depth(0), h.s.Depth(0)):
		t.Fatal("replayed order book differs")
	case fresh.lastOrder != h.s.lastOrder || fresh.lastSeq != h.s.lastSeq || len(fresh.dedupe) != len(h.s.dedupe):
		t.Fatal("replayed counters or dedupe table differ")
	}
}

// TestRandomCommandsKeepMoneyCorrect is the plan's week-3 property test:
// "every generated command preserves account and order invariants."
//
// It sends 3,000 random commands (funding, orders, cancels, exact retries
// and deliberate key conflicts) from four users trading in a narrow price
// band, so orders cross, partially fill, improve on price and hit
// self-trade prevention all the time. After EVERY command:
//
//   - the harness checks all invariants and that the ledger explains every
//     balance (see harness.check);
//   - an INDEPENDENT wealth model must agree with the state. It knows only
//     funding and fills ("Alice bought 0.1 BTC for 6,000 USDT") and never
//     looks at reservations, releases or journals, so a bug in any of
//     those shows up as a wealth mismatch;
//   - a retried request must return exactly its original result.
//
// The seed is fixed, so every run is identical.
func TestRandomCommandsKeepMoneyCorrect(t *testing.T) {
	const steps = 3_000
	rng := rand.New(rand.NewPCG(2026, 19))
	h := newHarness(t)
	users := []domain.UserID{1, 2, 3, 4}

	wealth := map[userAsset]int64{}
	type sent struct {
		user   domain.UserID
		key    string
		body   command.Body
		result command.Result
	}
	var history []sent
	count := map[string]int{}

	for step := 0; step < steps; step++ {
		user := users[rng.IntN(len(users))]
		sender, key := user, fmt.Sprintf("r%d", step)
		var body command.Body
		var retryOf *sent
		wantConflict := false

		switch roll := rng.IntN(100); {
		case roll < 8: // fund someone
			sender = operator
			body = command.Fund{To: user, Asset: domain.USDT, Amount: usdt(1_000 + rng.Int64N(50_000))}
			if rng.IntN(2) == 0 {
				body = command.Fund{To: user, Asset: domain.BTC, Amount: 1_000 + rng.Int64N(20_000)} // 0.1–2.1 BTC
			}
		case roll < 75: // place an order
			p := command.PlaceOrder{
				Side:  domain.Buy,
				Price: domain.Price(59_990 + rng.Int64N(21)),
				Qty:   domain.Qty(1 + rng.Int64N(5_000)),
				TIF:   domain.GTC,
			}
			if rng.IntN(2) == 0 {
				p.Side = domain.Sell
			}
			if rng.IntN(4) == 0 {
				p.TIF = domain.IOC
			}
			body = p
		case roll < 88: // cancel: usually one of the user's own orders
			var own []domain.OrderID
			for id, o := range h.s.open {
				if o.user == user {
					own = append(own, id)
				}
			}
			slices.Sort(own) // map order is random; sort so the run is reproducible
			id := domain.OrderID(1 + rng.Int64N(int64(h.s.lastOrder)+5))
			if len(own) > 0 && rng.IntN(4) != 0 {
				id = own[rng.IntN(len(own))]
			}
			body = command.CancelOrder{Order: id}
		case roll < 96 && len(history) > 0: // retry an earlier request exactly
			retryOf = &history[rng.IntN(len(history))]
			sender, key, body = retryOf.user, retryOf.key, retryOf.body
		case len(history) > 0: // reuse an earlier key for a different request
			prev := history[rng.IntN(len(history))]
			sender, key, body = prev.user, prev.key, command.CancelOrder{Order: 999_999}
			wantConflict = true
		default:
			continue
		}

		fx := h.apply(sender, key, body)
		res := fx.Result

		switch {
		case retryOf != nil:
			got := res
			got.Duplicate = false
			if !res.Duplicate || !reflect.DeepEqual(got, retryOf.result) {
				t.Fatalf("step %d: retry of key %q returned\n %+v\nwant the original\n %+v", step, key, res, retryOf.result)
			}
			count["exact retries"]++
			continue
		case wantConflict:
			if res.Rejected != command.RejectKeyConflict {
				t.Fatalf("step %d: reused key %q for a new request: %+v, want RejectKeyConflict", step, key, res)
			}
			count["key conflicts"]++
			continue
		}
		history = append(history, sent{sender, key, body, res})

		// Update the independent wealth model from the outcome alone.
		if res.Accepted() {
			switch b := body.(type) {
			case command.Fund:
				wealth[userAsset{b.To, b.Asset}] += b.Amount
				count["fundings"]++
			case command.PlaceOrder:
				for _, f := range res.Fills {
					buyer, seller := f.TakerUser, f.MakerUser
					if f.TakerSide == domain.Sell {
						buyer, seller = seller, buyer
					}
					q, cost := int64(f.Qty), int64(f.Price)*int64(f.Qty)*100
					wealth[userAsset{buyer, domain.BTC}] += q
					wealth[userAsset{buyer, domain.USDT}] -= cost
					wealth[userAsset{seller, domain.BTC}] -= q
					wealth[userAsset{seller, domain.USDT}] += cost
				}
				count["fills"] += len(res.Fills)
				count[res.Outcome.String()]++
				released := slices.ContainsFunc(fx.Journals, func(j Journal) bool { return j.Kind == Release })
				if released && (res.Outcome == book.Filled || res.Outcome == book.Rested) {
					count["price-improvement releases"]++
				}
			case command.CancelOrder:
				count["cancels"]++
			}
		} else {
			count[res.Rejected.String()]++
		}

		for _, u := range users {
			for _, a := range []domain.Asset{domain.BTC, domain.USDT} {
				avail, reserved := h.s.Balance(u, a)
				if avail+reserved != wealth[userAsset{u, a}] {
					t.Fatalf("step %d: user %d owns %s %v, but funding and fills say %s",
						step, u, a.Format(avail+reserved), a, a.Format(wealth[userAsset{u, a}]))
				}
			}
		}
	}

	assertReplayIsIdentical(t, h)

	t.Logf("%d steps, %d orders resting at the end: %v", steps, h.s.book.Len(), count)
	for _, path := range []string{
		"fundings", "fills", "price-improvement releases", book.Rested.String(), book.Filled.String(),
		book.CanceledIOC.String(), book.CanceledSelfTrade.String(), "cancels", "exact retries", "key conflicts",
		command.RejectInsufficientFunds.String(), command.RejectUnknownOrder.String(),
	} {
		if count[path] == 0 {
			t.Errorf("the random mix never exercised %q; adjust the probabilities", path)
		}
	}
}
