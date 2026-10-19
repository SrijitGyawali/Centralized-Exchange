package state

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// --- test harness -----------------------------------------------------------

const (
	operator domain.UserID = 99 // sends Fund commands
	alice    domain.UserID = 1
	bob      domain.UserID = 2
)

// harness drives a State the way the engine will: one command at a time,
// with increasing Seq. After EVERY command it checks two things:
//  1. all invariants hold (CheckInvariants), and
//  2. replaying every journal ever produced gives exactly the current
//     balances. The ledger fully explains every unit of money.
type harness struct {
	t        *testing.T
	s        *State
	seq      domain.Seq
	journals []Journal
	cmds     []command.Command // every command applied, for replay checks
	effects  []Effects         // what each one produced
}

func newHarness(t *testing.T) *harness { return &harness{t: t, s: New()} }

// apply sends one command with an explicit idempotency key.
func (h *harness) apply(user domain.UserID, key string, body command.Body) Effects {
	h.t.Helper()
	h.seq++
	cmd := command.Command{Seq: h.seq, User: user, Key: key, Body: body}
	fx, err := h.s.Apply(cmd)
	if err != nil {
		h.t.Fatalf("Apply(seq %d, %#v): %v", h.seq, body, err)
	}
	h.cmds = append(h.cmds, cmd)
	h.effects = append(h.effects, Effects{Result: fx.Result.Clone(), Journals: fx.Journals})
	for i, j := range fx.Journals {
		if j.Seq != h.seq || j.Index != i {
			h.t.Fatalf("journal %d has identity (%d, %d), want (%d, %d)", i, j.Seq, j.Index, h.seq, i)
		}
		if !j.Balanced() {
			h.t.Fatalf("unbalanced journal: %+v", j)
		}
	}
	h.journals = append(h.journals, fx.Journals...)
	h.check()
	return fx
}

// do sends a command with a fresh, never-used key and returns its result.
func (h *harness) do(user domain.UserID, body command.Body) command.Result {
	h.t.Helper()
	return h.apply(user, fmt.Sprintf("auto-%d", h.seq+1), body).Result
}

func (h *harness) check() {
	h.t.Helper()
	if err := h.s.CheckInvariants(); err != nil {
		h.t.Fatalf("after seq %d: %v", h.seq, err)
	}
	replayed := map[balanceKey]int64{}
	for _, j := range h.journals {
		for _, p := range j.Postings {
			replayed[balanceKey{p.Account.User, p.Asset, p.Account.Bucket}] += p.Amount
		}
	}
	maps.DeleteFunc(replayed, func(_ balanceKey, v int64) bool { return v == 0 })
	if !maps.Equal(replayed, h.s.balances) {
		h.t.Fatalf("after seq %d: the ledger does not explain the balances\nledger:   %v\nbalances: %v", h.seq, replayed, h.s.balances)
	}
}

func (h *harness) fund(to domain.UserID, asset domain.Asset, amount int64) {
	h.t.Helper()
	if r := h.do(operator, command.Fund{To: to, Asset: asset, Amount: amount}); !r.Accepted() {
		h.t.Fatalf("funding rejected: %v (%s)", r.Rejected, r.Detail)
	}
}

func (h *harness) wantBalance(user domain.UserID, asset domain.Asset, available, reserved int64) {
	h.t.Helper()
	a, r := h.s.Balance(user, asset)
	if a != available || r != reserved {
		h.t.Errorf("user %d %v: available %s reserved %s; want %s / %s",
			user, asset, asset.Format(a), asset.Format(r), asset.Format(available), asset.Format(reserved))
	}
}

func usdt(n int64) int64 { return n * domain.AtomsPerUSDT }

func buy(price, qty int64, tif domain.TimeInForce) command.PlaceOrder {
	return command.PlaceOrder{Side: domain.Buy, Price: domain.Price(price), Qty: domain.Qty(qty), TIF: tif}
}

func sell(price, qty int64, tif domain.TimeInForce) command.PlaceOrder {
	return command.PlaceOrder{Side: domain.Sell, Price: domain.Price(price), Qty: domain.Qty(qty), TIF: tif}
}

func kinds(js []Journal) []JournalKind {
	var ks []JournalKind
	for _, j := range js {
		ks = append(ks, j.Kind)
	}
	return ks
}

// --- funding ----------------------------------------------------------------

func TestFundCreditsAvailableFromIssuance(t *testing.T) {
	h := newHarness(t)
	fx := h.apply(operator, "f1", command.Fund{To: alice, Asset: domain.USDT, Amount: usdt(10_000)})

	if !fx.Result.Accepted() {
		t.Fatalf("rejected: %v", fx.Result.Rejected)
	}
	h.wantBalance(alice, domain.USDT, usdt(10_000), 0)
	if got := h.s.Issued(domain.USDT); got != usdt(10_000) {
		t.Errorf("Issued(USDT) = %d, want %d", got, usdt(10_000))
	}
	want := []Posting{
		{Account{SystemUser, Issuance}, domain.USDT, -usdt(10_000)},
		{Account{alice, Available}, domain.USDT, usdt(10_000)},
	}
	if len(fx.Journals) != 1 || fx.Journals[0].Kind != Funding || !slices.Equal(fx.Journals[0].Postings, want) {
		t.Errorf("journals = %+v, want one funding journal %+v", fx.Journals, want)
	}
}

func TestFundRejections(t *testing.T) {
	tests := []struct {
		name string
		body command.Fund
		want command.RejectReason
	}{
		{"no recipient", command.Fund{To: 0, Asset: domain.USDT, Amount: 1}, command.RejectInvalid},
		{"system recipient", command.Fund{To: SystemUser, Asset: domain.BTC, Amount: 1}, command.RejectInvalid},
		{"unset asset", command.Fund{To: alice, Amount: 1}, command.RejectInvalid},
		{"zero amount", command.Fund{To: alice, Asset: domain.USDT, Amount: 0}, command.RejectInvalid},
		{"negative amount", command.Fund{To: alice, Asset: domain.USDT, Amount: -5}, command.RejectInvalid},
		{"above max supply", command.Fund{To: alice, Asset: domain.BTC, Amount: domain.BTC.MaxSupply() + 1}, command.RejectSupplyCap},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			fx := h.apply(operator, "f", tc.body)
			if fx.Result.Rejected != tc.want {
				t.Errorf("Rejected = %v, want %v", fx.Result.Rejected, tc.want)
			}
			if len(fx.Journals) != 0 || len(h.s.balances) != 0 {
				t.Errorf("a rejected funding changed money: journals %+v, balances %v", fx.Journals, h.s.balances)
			}
		})
	}
}

func TestSupplyCapIsExact(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.BTC, domain.BTC.MaxSupply()-1)
	h.fund(bob, domain.BTC, 1) // exactly reaches the cap: allowed
	if r := h.do(operator, command.Fund{To: bob, Asset: domain.BTC, Amount: 1}); r.Rejected != command.RejectSupplyCap {
		t.Errorf("one lot past the cap: Rejected = %v, want RejectSupplyCap", r.Rejected)
	}
}

// --- placing orders: reservations -------------------------------------------

func TestPlaceReservesFunds(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	h.fund(bob, domain.BTC, domain.LotsPerBTC) // 1 BTC

	// Alice bids 0.1 BTC at 60,000: she must reserve 6,000 USDT.
	r := h.do(alice, buy(60_000, 1_000, domain.GTC))
	if !r.Accepted() || r.Order != 1 || r.Outcome != book.Rested {
		t.Fatalf("result = %+v, want accepted order 1, rested", r)
	}
	h.wantBalance(alice, domain.USDT, usdt(4_000), usdt(6_000))

	// Bob asks 0.25 BTC at 61,000: he must reserve the 0.25 BTC itself.
	r = h.do(bob, sell(61_000, 2_500, domain.GTC))
	if !r.Accepted() || r.Order != 2 {
		t.Fatalf("result = %+v, want accepted order 2", r)
	}
	h.wantBalance(bob, domain.BTC, 7_500, 2_500)
}

func TestInsufficientFundsChangesNothing(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(5_000))

	fx := h.apply(alice, "too-big", buy(60_000, 1_000, domain.GTC)) // needs 6,000
	if fx.Result.Rejected != command.RejectInsufficientFunds {
		t.Fatalf("Rejected = %v, want RejectInsufficientFunds", fx.Result.Rejected)
	}
	if !strings.Contains(fx.Result.Detail, "6000.000000") {
		t.Errorf("Detail %q should say how much was needed", fx.Result.Detail)
	}
	if len(fx.Journals) != 0 || h.s.book.Len() != 0 {
		t.Errorf("rejected order left traces: journals %+v, book len %d", fx.Journals, h.s.book.Len())
	}
	h.wantBalance(alice, domain.USDT, usdt(5_000), 0)

	if r := h.do(bob, sell(60_000, 1, domain.GTC)); r.Rejected != command.RejectInsufficientFunds {
		t.Errorf("sell without BTC: Rejected = %v, want RejectInsufficientFunds", r.Rejected)
	}
	// Rejections assign no order ID: the next accepted order still gets #1.
	h.fund(alice, domain.USDT, usdt(1_000))
	if r := h.do(alice, buy(60_000, 1_000, domain.GTC)); r.Order != 1 {
		t.Errorf("first accepted order got ID %d, want 1", r.Order)
	}
}

func TestInvalidOrdersRejected(t *testing.T) {
	tests := []struct {
		name string
		body command.PlaceOrder
	}{
		{"unset side", command.PlaceOrder{Price: 60_000, Qty: 1, TIF: domain.GTC}},
		{"unset tif", command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1}},
		{"zero price", buy(0, 1, domain.GTC)},
		{"price above max", buy(int64(domain.MaxPrice)+1, 1, domain.GTC)},
		{"zero qty", buy(60_000, 0, domain.GTC)},
		{"qty above max", sell(60_000, int64(domain.MaxQty)+1, domain.GTC)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.fund(alice, domain.USDT, usdt(1_000_000))
			fx := h.apply(alice, "k", tc.body)
			if fx.Result.Rejected != command.RejectInvalid || len(fx.Journals) != 0 {
				t.Errorf("result %+v, journals %d; want RejectInvalid and no journals", fx.Result, len(fx.Journals))
			}
		})
	}
}

// --- settlement ---------------------------------------------------------------

// TestSettlementWorkedExample is the plan's Alice-and-Bob example with zero
// fees: Alice buys 0.1 BTC from Bob at 60,000. Every posting is checked.
func TestSettlementWorkedExample(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	h.fund(bob, domain.BTC, domain.LotsPerBTC)

	h.do(bob, sell(60_000, 1_000, domain.GTC)) // order 1 rests
	fx := h.apply(alice, "a", buy(60_000, 1_000, domain.GTC))

	if fx.Result.Outcome != book.Filled || len(fx.Result.Fills) != 1 {
		t.Fatalf("result = %+v, want filled with one fill", fx.Result)
	}
	if got := kinds(fx.Journals); !slices.Equal(got, []JournalKind{Reservation, Settlement}) {
		t.Fatalf("journal kinds = %v, want [reservation settlement] (exact price: nothing to release)", got)
	}
	settle := fx.Journals[1]
	wantPostings := []Posting{
		{Account{alice, Reserved}, domain.USDT, -usdt(6_000)},
		{Account{bob, Available}, domain.USDT, usdt(6_000)},
		{Account{bob, Reserved}, domain.BTC, -1_000},
		{Account{alice, Available}, domain.BTC, 1_000},
	}
	if !slices.Equal(settle.Postings, wantPostings) || settle.Order != 2 || settle.Maker != 1 {
		t.Errorf("settlement = %+v\nwant postings %+v for taker 2, maker 1", settle, wantPostings)
	}

	h.wantBalance(alice, domain.USDT, usdt(4_000), 0)
	h.wantBalance(alice, domain.BTC, 1_000, 0)
	h.wantBalance(bob, domain.USDT, usdt(6_000), 0)
	h.wantBalance(bob, domain.BTC, 9_000, 0)
}

// TestPriceImprovementIsReleased: Alice will pay up to 60,200, so she
// reserves 0.1 × 60,200 = 6,020 USDT. She actually buys at Bob's 60,100,
// paying 6,010. The 10 USDT difference must come back to her.
func TestPriceImprovementIsReleased(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	h.fund(bob, domain.BTC, domain.LotsPerBTC)
	h.do(bob, sell(60_100, 1_000, domain.GTC))

	fx := h.apply(alice, "a", buy(60_200, 1_000, domain.GTC))

	if got := kinds(fx.Journals); !slices.Equal(got, []JournalKind{Reservation, Settlement, Release}) {
		t.Fatalf("journal kinds = %v, want [reservation settlement release]", got)
	}
	if got := fx.Journals[2].Postings[1].Amount; got != usdt(10) {
		t.Errorf("released %d atoms, want 10 USDT", got)
	}
	h.wantBalance(alice, domain.USDT, usdt(10_000-6_010), 0)
}

// TestPartialFillKeepsExactReserve: Alice bids 0.3 BTC at 60,000 (reserves
// 18,000). Bob sells her 0.1, so her remaining 0.2 must still be backed by
// exactly 12,000. Bob's IOC sell trades at Alice's (maker) price.
func TestPartialFillKeepsExactReserve(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(20_000))
	h.fund(bob, domain.BTC, domain.LotsPerBTC)
	h.do(alice, buy(60_000, 3_000, domain.GTC))

	r := h.do(bob, sell(59_000, 1_000, domain.IOC))
	if r.Outcome != book.Filled || r.Fills[0].Price != 60_000 {
		t.Fatalf("result = %+v, want filled at the maker's 60,000", r)
	}
	h.wantBalance(alice, domain.USDT, usdt(2_000), usdt(12_000))
	h.wantBalance(alice, domain.BTC, 1_000, 0)
	h.wantBalance(bob, domain.USDT, usdt(6_000), 0)
	h.wantBalance(bob, domain.BTC, 9_000, 0)
}

func TestCanceledRemaindersAreReleased(t *testing.T) {
	t.Run("ioc remainder", func(t *testing.T) {
		h := newHarness(t)
		h.fund(alice, domain.USDT, usdt(20_000))
		h.fund(bob, domain.BTC, domain.LotsPerBTC)
		h.do(bob, sell(60_000, 1_000, domain.GTC))

		r := h.do(alice, buy(60_000, 3_000, domain.IOC)) // wants 0.3, only 0.1 available
		if r.Outcome != book.CanceledIOC || r.Remaining != 2_000 {
			t.Fatalf("result = %+v, want canceled ioc with 0.2 remaining", r)
		}
		h.wantBalance(alice, domain.USDT, usdt(14_000), 0) // paid 6,000; the other 12,000 released
	})

	t.Run("self-trade", func(t *testing.T) {
		h := newHarness(t)
		h.fund(alice, domain.USDT, usdt(10_000))
		h.fund(alice, domain.BTC, domain.LotsPerBTC)
		h.do(alice, sell(60_000, 1_000, domain.GTC)) // her own ask

		r := h.do(alice, buy(60_000, 1_000, domain.GTC))
		if r.Outcome != book.CanceledSelfTrade || len(r.Fills) != 0 {
			t.Fatalf("result = %+v, want self-trade prevented, no fills", r)
		}
		h.wantBalance(alice, domain.USDT, usdt(10_000), 0) // fully released
		h.wantBalance(alice, domain.BTC, 9_000, 1_000)     // her ask untouched
	})
}

// --- cancel -------------------------------------------------------------------

func TestCancelReleasesReservation(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	id := h.do(alice, buy(60_000, 1_000, domain.GTC)).Order

	fx := h.apply(alice, "c", command.CancelOrder{Order: id})
	if !fx.Result.Accepted() || fx.Result.Canceled != 1_000 {
		t.Fatalf("result = %+v, want accepted with 0.1 canceled", fx.Result)
	}
	if got := kinds(fx.Journals); !slices.Equal(got, []JournalKind{Release}) {
		t.Errorf("journal kinds = %v, want [release]", got)
	}
	h.wantBalance(alice, domain.USDT, usdt(10_000), 0)

	// A second cancel (with a new key) finds nothing: no double release.
	if r := h.do(alice, command.CancelOrder{Order: id}); r.Rejected != command.RejectUnknownOrder {
		t.Errorf("second cancel: Rejected = %v, want RejectUnknownOrder", r.Rejected)
	}
	h.wantBalance(alice, domain.USDT, usdt(10_000), 0)
}

func TestCannotCancelSomeoneElsesOrder(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	id := h.do(alice, buy(60_000, 1_000, domain.GTC)).Order

	r := h.do(bob, command.CancelOrder{Order: id})
	if r.Rejected != command.RejectUnknownOrder {
		t.Errorf("Rejected = %v, want RejectUnknownOrder (same answer as a missing order)", r.Rejected)
	}
	if _, ok := h.s.Order(id); !ok {
		t.Error("Bob's cancel removed Alice's order")
	}
}

func TestCancelAfterPartialFillReleasesOnlyTheRest(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(20_000))
	h.fund(bob, domain.BTC, domain.LotsPerBTC)
	id := h.do(alice, buy(60_000, 3_000, domain.GTC)).Order // reserves 18,000
	h.do(bob, sell(60_000, 1_000, domain.IOC))              // Alice pays 6,000

	r := h.do(alice, command.CancelOrder{Order: id})
	if r.Canceled != 2_000 {
		t.Errorf("Canceled = %d lots, want the remaining 2,000", r.Canceled)
	}
	h.wantBalance(alice, domain.USDT, usdt(14_000), 0) // 20,000 - 6,000 spent
}

// --- Apply's own errors -----------------------------------------------------

func TestApplyRejectsBadSequencing(t *testing.T) {
	s := New()
	body := command.Fund{To: alice, Asset: domain.USDT, Amount: 1}
	if _, err := s.Apply(command.Command{Seq: 1, User: operator, Key: "a", Body: body}); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []domain.Seq{1, 0} {
		_, err := s.Apply(command.Command{Seq: seq, User: operator, Key: "b", Body: body})
		if !errors.Is(err, ErrSeqNotIncreasing) {
			t.Errorf("Seq %d after 1: error = %v, want ErrSeqNotIncreasing", seq, err)
		}
	}
	if _, err := s.Apply(command.Command{Seq: 2, User: operator, Key: "c"}); err == nil {
		t.Error("a command with no Body was accepted")
	}
	if s.LastSeq() != 1 {
		t.Errorf("LastSeq = %d after rejected Applies, want 1", s.LastSeq())
	}
}

func TestIdempotencyKeyMustBeValid(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	for _, key := range []string{"", strings.Repeat("k", command.MaxKeyLen+1)} {
		fx := h.apply(alice, key, buy(60_000, 1_000, domain.GTC))
		if fx.Result.Rejected != command.RejectInvalid || len(fx.Journals) != 0 {
			t.Errorf("key of length %d: result %+v; want RejectInvalid, nothing done", len(key), fx.Result)
		}
	}
	// The longest allowed key works.
	if r := h.apply(alice, strings.Repeat("k", command.MaxKeyLen), buy(60_000, 1_000, domain.GTC)).Result; !r.Accepted() {
		t.Errorf("a %d-byte key was rejected: %+v", command.MaxKeyLen, r)
	}
}
