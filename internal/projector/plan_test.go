package projector

import (
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// These tests need no database: plan is a pure function from (command,
// effects) to SQL statements. Real effects come from a real state.State.

const (
	operator domain.UserID = 99
	alice    domain.UserID = 1
	bob      domain.UserID = 2
)

type applier struct {
	t   *testing.T
	st  *state.State
	seq domain.Seq
}

func (a *applier) do(user domain.UserID, key string, body command.Body) (command.Command, state.Effects) {
	a.t.Helper()
	a.seq++
	c := command.Command{Seq: a.seq, User: user, Key: key, Body: body}
	fx, err := a.st.Apply(c)
	if err != nil {
		a.t.Fatal(err)
	}
	return c, fx
}

// find returns the first statement whose SQL starts with prefix.
func find(t *testing.T, stmts []stmt, prefix string) stmt {
	t.Helper()
	for _, s := range stmts {
		if strings.HasPrefix(strings.TrimSpace(s.sql), prefix) {
			return s
		}
	}
	t.Fatalf("no statement starting with %q", prefix)
	return stmt{}
}

func mustPlan(t *testing.T, c command.Command, fx state.Effects) []stmt {
	t.Helper()
	s, err := plan(c, fx)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlanFunding(t *testing.T) {
	a := &applier{t: t, st: state.New()}
	c, fx := a.do(operator, "f", command.Fund{To: alice, Asset: domain.USDT, Amount: 5})

	got := mustPlan(t, c, fx)
	// commands row, one journal, two lines, two balance updates.
	if len(got) != 6 {
		t.Fatalf("%d statements, want 6", len(got))
	}
	if args := got[0].args; !slices.Equal(args, []any{int64(1), int64(99), "f", "fund", true, "", false}) {
		t.Errorf("commands row = %v", args)
	}
	if args := find(t, got, "insert into ledger_journals").args; args[2] != "funding" {
		t.Errorf("journal kind = %v, want funding", args[2])
	}
}

// TestPlanPartialFill: Bob asks 0.1 at 60,000; Alice bids 0.3 at 60,000.
// Alice's order is partially filled and rests; Bob's is filled.
func TestPlanPartialFill(t *testing.T) {
	a := &applier{t: t, st: state.New()}
	a.do(operator, "f1", command.Fund{To: alice, Asset: domain.USDT, Amount: 20_000 * domain.AtomsPerUSDT})
	a.do(operator, "f2", command.Fund{To: bob, Asset: domain.BTC, Amount: domain.LotsPerBTC})
	a.do(bob, "ask", command.PlaceOrder{Side: domain.Sell, Price: 60_000, Qty: 1_000, TIF: domain.GTC}) // order 1
	c, fx := a.do(alice, "bid", command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 3_000, TIF: domain.GTC})

	got := mustPlan(t, c, fx)
	order := find(t, got, "insert into orders").args
	// order_id, user, side, tif, price, qty, filled, open, status, seq
	want := []any{int64(2), int64(1), "buy", "gtc", int64(60_000), int64(3_000), int64(1_000), int64(2_000), "partially_filled", int64(4)}
	if !slices.Equal(order, want) {
		t.Errorf("orders row = %v\n want %v", order, want)
	}
	trade := find(t, got, "insert into trades").args
	if !slices.Equal(trade, []any{int64(4), 0, int64(2), int64(1), int64(1), int64(2), "buy", int64(60_000), int64(1_000)}) {
		t.Errorf("trades row = %v", trade)
	}
	maker := find(t, got, "update orders set filled_lots").args
	if !slices.Equal(maker, []any{int64(1_000), int64(0), int64(4), int64(1)}) {
		t.Errorf("maker update = %v; want +1000 filled, 0 open, seq 4, order 1", maker)
	}
}

func TestPlanOutcomeStatuses(t *testing.T) {
	tests := []struct {
		name string
		body command.PlaceOrder
		want string
	}{
		{"rests untouched", command.PlaceOrder{Side: domain.Buy, Price: 50_000, Qty: 1, TIF: domain.GTC}, "open"},
		{"ioc into nothing", command.PlaceOrder{Side: domain.Buy, Price: 50_000, Qty: 1, TIF: domain.IOC}, "canceled_ioc"},
	}
	for _, tc := range tests {
		a := &applier{t: t, st: state.New()}
		a.do(operator, "f", command.Fund{To: alice, Asset: domain.USDT, Amount: 100 * domain.AtomsPerUSDT})
		c, fx := a.do(alice, "o", tc.body)
		if got := find(t, mustPlan(t, c, fx), "insert into orders").args[8]; got != tc.want {
			t.Errorf("%s: status %v, want %s", tc.name, got, tc.want)
		}
	}
}

func TestPlanCancelDuplicateAndRejection(t *testing.T) {
	a := &applier{t: t, st: state.New()}
	a.do(operator, "f", command.Fund{To: alice, Asset: domain.USDT, Amount: 100 * domain.AtomsPerUSDT})
	a.do(alice, "o", command.PlaceOrder{Side: domain.Buy, Price: 50_000, Qty: 1, TIF: domain.GTC})

	c, fx := a.do(alice, "c", command.CancelOrder{Order: 1})
	if args := find(t, mustPlan(t, c, fx), "update orders set open_lots = 0").args; !slices.Equal(args, []any{int64(3), int64(1)}) {
		t.Errorf("cancel update = %v", args)
	}

	// A retried key: recorded as a command, nothing else (its effects were
	// recorded the first time).
	c, fx = a.do(alice, "c", command.CancelOrder{Order: 1})
	if got := mustPlan(t, c, fx); len(got) != 1 || got[0].args[6] != true {
		t.Errorf("duplicate produced %d statements, want only its commands row marked duplicate", len(got))
	}

	// A rejection: recorded with its reason, nothing else.
	c, fx = a.do(alice, "big", command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 100_000, TIF: domain.GTC})
	if got := mustPlan(t, c, fx); len(got) != 1 || got[0].args[5] != "insufficient funds" {
		t.Errorf("rejection produced %v", got)
	}
}

func TestPlanRefusesIDsBeyondBigint(t *testing.T) {
	c := command.Command{Seq: 1, User: math.MaxUint64, Key: "k", Body: command.CancelOrder{Order: 1}}
	if _, err := plan(c, state.Effects{}); err == nil {
		t.Error("a user ID above MaxInt64 was accepted; it would wrap to a negative BIGINT")
	}
}
