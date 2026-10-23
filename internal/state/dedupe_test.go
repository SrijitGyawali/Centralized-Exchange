package state

import (
	"maps"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

func TestLookupIsPrivateImmutableAndReadOnly(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10000))
	h.fund(bob, domain.BTC, domain.LotsPerBTC)
	h.do(bob, sell(60000, 1000, domain.GTC))
	original := h.apply(alice, "lost-response", buy(60000, 1000, domain.GTC)).Result
	before := h.s.Digest()
	r, ok := h.s.Lookup(alice, "lost-response")
	if !ok || r.Seq != original.Seq || r.Order != original.Order || r.Duplicate || len(r.Fills) != 1 {
		t.Fatalf("lookup: %+v, %v", r, ok)
	}
	r.Fills[0].Qty = 1
	again, _ := h.s.Lookup(alice, "lost-response")
	if again.Fills[0].Qty != 1000 {
		t.Fatal("lookup leaked mutable fills")
	}
	if _, ok := h.s.Lookup(bob, "lost-response"); ok {
		t.Fatal("lookup leaked another user's result")
	}
	if _, ok := h.s.Lookup(alice, "missing"); ok {
		t.Fatal("missing key found")
	}
	if before != h.s.Digest() {
		t.Fatal("lookup changed state")
	}
	rejected := h.apply(alice, "reject", buy(60000, 1000, domain.GTC)).Result
	stored, ok := h.s.Lookup(alice, "reject")
	if !ok || stored.Rejected != rejected.Rejected || stored.Accepted() {
		t.Fatal("rejection was not recoverable")
	}
}

// These tests cover "handling repeated requests safely". The story behind
// all of them: a client sends a request, the exchange carries it out, and
// the response is lost on the network. The client cannot know whether it
// worked, so it sends the SAME request again with the SAME idempotency key.
// That retry must never do the work twice.

func TestRetryReturnsTheOriginalResult(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	first := h.apply(alice, "order-1", buy(60_000, 1_000, domain.GTC))

	balancesBefore := maps.Clone(h.s.balances)
	retry := h.apply(alice, "order-1", buy(60_000, 1_000, domain.GTC))

	if !retry.Result.Duplicate {
		t.Error("retry not marked Duplicate")
	}
	if retry.Result.Seq != first.Result.Seq || retry.Result.Order != first.Result.Order || retry.Result.Outcome != first.Result.Outcome {
		t.Errorf("retry result %+v differs from the original %+v", retry.Result, first.Result)
	}
	if len(retry.Journals) != 0 {
		t.Errorf("retry produced journals %+v; it must not move money", retry.Journals)
	}
	if !maps.Equal(h.s.balances, balancesBefore) || h.s.book.Len() != 1 {
		t.Error("retry changed balances or placed a second order")
	}
}

// The stored result is the answer to "what happened to MY request?", not a
// live status. Even after the order later fills, a retry still reports the
// original outcome (rested). Status queries are a separate API (week 5).
func TestRetryAfterStateChangedStillReturnsTheOriginal(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	h.fund(bob, domain.BTC, domain.LotsPerBTC)
	h.apply(alice, "order-1", buy(60_000, 1_000, domain.GTC))
	h.do(bob, sell(60_000, 1_000, domain.GTC)) // fills Alice's order

	retry := h.apply(alice, "order-1", buy(60_000, 1_000, domain.GTC)).Result
	if retry.Outcome != book.Rested || !retry.Duplicate {
		t.Errorf("retry = %+v, want the original 'rested' result", retry)
	}
}

func TestSameKeyDifferentRequestIsAConflict(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(20_000))
	h.apply(alice, "order-1", buy(60_000, 1_000, domain.GTC))

	fx := h.apply(alice, "order-1", buy(60_000, 2_000, domain.GTC)) // different qty
	if fx.Result.Rejected != command.RejectKeyConflict || len(fx.Journals) != 0 {
		t.Fatalf("result %+v, journals %d; want RejectKeyConflict, nothing done", fx.Result, len(fx.Journals))
	}
	if h.s.book.Len() != 1 {
		t.Errorf("book has %d orders; the conflicting request must not be placed", h.s.book.Len())
	}
	// The key still belongs to the ORIGINAL request.
	if r := h.apply(alice, "order-1", buy(60_000, 1_000, domain.GTC)).Result; !r.Duplicate || r.Order != 1 {
		t.Errorf("original retried after a conflict: %+v, want a duplicate of order 1", r)
	}
}

func TestKeysBelongToOneUser(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	h.fund(bob, domain.USDT, usdt(10_000))

	a := h.apply(alice, "k1", buy(60_000, 1_000, domain.GTC)).Result
	b := h.apply(bob, "k1", buy(60_000, 1_000, domain.GTC)).Result
	if a.Duplicate || b.Duplicate || a.Order == b.Order {
		t.Errorf("two users' identical keys interfered: alice %+v, bob %+v", a, b)
	}
}

// Rejections are decisions too: they are remembered and replayed. If Alice
// is rejected for insufficient funds and is funded afterwards, retrying the
// SAME key still returns the rejection; she must use a new key for a new
// attempt. Otherwise a "retry" could silently become a different trade from
// the one the client was told about.
func TestRejectionsAreRemembered(t *testing.T) {
	h := newHarness(t)
	h.apply(alice, "order-1", buy(60_000, 1_000, domain.GTC)) // no money: rejected
	h.fund(alice, domain.USDT, usdt(10_000))

	retry := h.apply(alice, "order-1", buy(60_000, 1_000, domain.GTC)).Result
	if retry.Rejected != command.RejectInsufficientFunds || !retry.Duplicate {
		t.Errorf("retry = %+v, want the remembered insufficient-funds rejection", retry)
	}
	if r := h.apply(alice, "order-2", buy(60_000, 1_000, domain.GTC)).Result; !r.Accepted() {
		t.Errorf("a new key after funding was rejected: %+v", r)
	}
}

// The guarantee that matters most for money: retrying a cancel must never
// release the same reservation twice.
func TestCancelRetryDoesNotReleaseTwice(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	id := h.do(alice, buy(60_000, 1_000, domain.GTC)).Order

	h.apply(alice, "cancel-1", command.CancelOrder{Order: id})
	retry := h.apply(alice, "cancel-1", command.CancelOrder{Order: id})

	if !retry.Result.Duplicate || len(retry.Journals) != 0 {
		t.Errorf("retry = %+v with %d journals, want a duplicate with none", retry.Result, len(retry.Journals))
	}
	h.wantBalance(alice, domain.USDT, usdt(10_000), 0) // released exactly once
}

// The stored result must be protected from callers: editing the Fills of a
// returned duplicate must not change what the next retry sees.
func TestDuplicateResultsAreCopies(t *testing.T) {
	h := newHarness(t)
	h.fund(alice, domain.USDT, usdt(10_000))
	h.fund(bob, domain.BTC, domain.LotsPerBTC)
	h.do(bob, sell(60_000, 1_000, domain.GTC))
	h.apply(alice, "k", buy(60_000, 1_000, domain.GTC))

	first := h.apply(alice, "k", buy(60_000, 1_000, domain.GTC)).Result
	first.Fills[0].Qty = 1 // scribble on the returned copy

	second := h.apply(alice, "k", buy(60_000, 1_000, domain.GTC)).Result
	if second.Fills[0].Qty != 1_000 {
		t.Errorf("stored result was changed through a returned copy: fill qty %d", second.Fills[0].Qty)
	}
}
