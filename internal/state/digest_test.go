package state

import (
	"slices"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// script builds a small trading history: funding, a fill with price
// improvement, a resting order, a partial fill and a cancel.
func script(h *harness) {
	h.fund(alice, domain.USDT, usdt(100_000))
	h.fund(bob, domain.BTC, 5*domain.LotsPerBTC)
	h.do(bob, sell(60_000, 2_000, domain.GTC))
	h.do(alice, buy(60_100, 1_000, domain.GTC))
	h.do(alice, buy(59_000, 3_000, domain.GTC))
	h.do(bob, sell(58_000, 1_000, domain.IOC))
	h.do(alice, command.CancelOrder{Order: 3})
}

func TestDigestIsDeterministic(t *testing.T) {
	a, b := newHarness(t), newHarness(t)
	script(a)
	script(b)
	if a.s.Digest() != b.s.Digest() {
		t.Fatal("the same commands produced different digests")
	}
	// Asking twice must not change the answer: map iteration order is
	// random in Go, and the digest must not depend on it.
	for range 20 {
		if a.s.Digest() != b.s.Digest() {
			t.Fatal("digest changed between calls")
		}
	}
}

// TestDigestSeesWhatOtherViewsMiss: the digest must change even when the
// difference is invisible in balances and depth.
func TestDigestSeesWhatOtherViewsMiss(t *testing.T) {
	t.Run("a remembered rejection", func(t *testing.T) {
		a, b := newHarness(t), newHarness(t)
		script(a)
		script(b)
		b.do(alice, command.CancelOrder{Order: 999}) // rejected: only dedupe and lastSeq change
		if a.s.Digest() == b.s.Digest() {
			t.Error("an extra remembered request did not change the digest")
		}
	})

	t.Run("queue order at one price", func(t *testing.T) {
		// Two bids at 50,000 placed in opposite orders: identical balances
		// and identical depth, but different time priority.
		bobFirst, aliceFirst := newHarness(t), newHarness(t)
		for _, h := range []*harness{bobFirst, aliceFirst} {
			script(h)
			h.fund(bob, domain.USDT, usdt(100_000))
		}
		bobFirst.do(bob, buy(50_000, 1, domain.GTC))
		bobFirst.do(alice, buy(50_000, 1, domain.GTC))
		aliceFirst.do(alice, buy(50_000, 1, domain.GTC))
		aliceFirst.do(bob, buy(50_000, 1, domain.GTC))

		if !slices.Equal(bobFirst.s.Balances(), aliceFirst.s.Balances()) ||
			!slices.Equal(bobFirst.s.Depth(0).Bids, aliceFirst.s.Depth(0).Bids) {
			t.Fatal("setup: balances and depth were supposed to be identical")
		}
		if bobFirst.s.Digest() == aliceFirst.s.Digest() {
			t.Error("different FIFO orders produced the same digest")
		}
	})
}

func TestBalancesListing(t *testing.T) {
	h := newHarness(t)
	h.fund(bob, domain.BTC, domain.LotsPerBTC)
	h.fund(alice, domain.USDT, usdt(10_000))
	h.do(alice, buy(60_000, 1_000, domain.GTC)) // Alice: 4,000 available, 6,000 reserved

	want := []BalanceRow{
		{User: alice, Asset: domain.USDT, Available: usdt(4_000), Reserved: usdt(6_000)},
		{User: bob, Asset: domain.BTC, Available: domain.LotsPerBTC},
	}
	if got := h.s.Balances(); !slices.Equal(got, want) {
		t.Errorf("Balances() = %+v, want %+v (sorted, no issuance account)", got, want)
	}
}
