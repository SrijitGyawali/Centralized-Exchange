package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/node"
)

// explainJournal records a scenario that exercises every rule the
// inspector explains, through a real node (journal on disk):
//
//	seq 1-3  funding: user 1 USDT, users 2 and 3 BTC
//	seq 4    user 2 sells 0.1 @ 60000           (rests)
//	seq 5    user 3 sells 0.1 @ 60000           (rests, same price, LATER)
//	seq 6    user 2 sells 0.1 @ 59900           (rests, BETTER price)
//	seq 7    user 1 buys 0.25 limit 60050 GTC: fills 59900 first (price),
//	         then seq 4 before seq 5 at 60000 (time), each at the maker's
//	         price (improvement); seq 5's order keeps 0.05
//	seq 8    user 3 cancels its order (the 0.05 left)
//	seq 9    user 1 retries seq 7's request with the same key: a duplicate
func explainJournal(t *testing.T) (dir string, ids map[string]domain.OrderID, digest [32]byte) {
	t.Helper()
	dir = t.TempDir()
	n, err := node.Open(node.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ids = map[string]domain.OrderID{}
	submit := func(name string, user domain.UserID, key string, body command.Body) {
		t.Helper()
		res, err := n.Engine.Submit(t.Context(), user, key, body)
		if err != nil || !res.Accepted() {
			t.Fatalf("%s: %+v %v", name, res, err)
		}
		if res.Order != 0 && !res.Duplicate {
			ids[name] = res.Order
		}
	}
	submit("fund1", 99, "f1", command.Fund{To: 1, Asset: domain.USDT, Amount: 100_000 * domain.AtomsPerUSDT})
	submit("fund2", 99, "f2", command.Fund{To: 2, Asset: domain.BTC, Amount: domain.LotsPerBTC})
	submit("fund3", 99, "f3", command.Fund{To: 3, Asset: domain.BTC, Amount: domain.LotsPerBTC})
	submit("old", 2, "s1", command.PlaceOrder{Side: domain.Sell, Price: 60_000, Qty: 1_000, TIF: domain.GTC})
	submit("young", 3, "s2", command.PlaceOrder{Side: domain.Sell, Price: 60_000, Qty: 1_000, TIF: domain.GTC})
	submit("cheap", 2, "s3", command.PlaceOrder{Side: domain.Sell, Price: 59_900, Qty: 1_000, TIF: domain.GTC})
	buy := command.PlaceOrder{Side: domain.Buy, Price: 60_050, Qty: 2_500, TIF: domain.GTC}
	submit("taker", 1, "b1", buy)
	submit("cancel", 3, "c1", command.CancelOrder{Order: ids["young"]})
	submit("retry", 1, "b1", buy)
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := node.Open(node.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	return dir, ids, again.Recovery.Digest
}

func explain(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := run(args, &out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

// mustContainInOrder fails unless every want appears in out, in this order.
func mustContainInOrder(t *testing.T, out string, wants ...string) {
	t.Helper()
	rest := out
	for _, w := range wants {
		i := strings.Index(rest, w)
		if i < 0 {
			t.Fatalf("missing (or out of order) %q in:\n%s", w, out)
		}
		rest = rest[i+len(w):]
	}
}

// TestExplainTheTaker: the fills are listed in priority order with the
// reason (price first, then the older order), at the makers' prices.
func TestExplainTheTaker(t *testing.T) {
	dir, ids, digest := explainJournal(t)
	out := explain(t, "-data-dir", dir, "-explain", fmt.Sprint(ids["taker"]))
	mustContainInOrder(t, out,
		fmt.Sprintf("order      %d", ids["taker"]),
		"rules      Tier 1",
		`request    seq 7, user 1, key "b1": buy 0.2500 BTC limit 60050, GTC`,
		"reserve    15012.500000 USDT at the limit price", // 0.25 x 60050
		"before: BTC 0.0000 available, 0.0000 reserved; USDT 100000.000000 available",
		"matching   3 fill(s)",
		fmt.Sprintf("1. 0.1000 BTC @ 59900 against order %d (user 2, resting since seq 6), 150 USDT better", ids["cheap"]),
		fmt.Sprintf("2. 0.1000 BTC @ 60000 against order %d (user 2, resting since seq 4), 50 USDT better", ids["old"]),
		fmt.Sprintf("3. 0.0500 BTC @ 60000 against order %d (user 3, resting since seq 5)", ids["young"]),
		"outcome    filled; 0.0000 BTC remaining",
		"status     filled completely",
		"seq 7 #0 reservation",
		"seq 7 #1 settlement",
		"seq 7 #2 settlement",
		"seq 7 #3 settlement",
		"seq 7 #4 release", // the price improvement handed back
		"invariants ok",
		fmt.Sprintf("digest     %x", digest),
	)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "  seq 9") { // a story line (later events, ledger), not the footer
			t.Errorf("the retry (seq 9) changed nothing and must not appear in the order's story: %q", line)
		}
	}
}

// TestExplainAMakerThatWasHitThenCanceled: the later part of the story.
func TestExplainAMakerThatWasHitThenCanceled(t *testing.T) {
	dir, ids, _ := explainJournal(t)
	out := explain(t, "-data-dir", dir, "-explain", fmt.Sprint(ids["young"]))
	mustContainInOrder(t, out,
		`request    seq 5, user 3, key "s2": sell 0.1000 BTC limit 60000, GTC`,
		"reserve    0.1000 BTC at the limit price",
		"matching   no opposite order at or better than 60000",
		"outcome    rested; 0.1000 BTC remaining",
		fmt.Sprintf("seq 7  hit by order %d (user 1, buy): 0.0500 BTC @ 60000, 0.0500 BTC left", ids["taker"]),
		`seq 8  canceled by user 3 (key "c1"): 0.0500 BTC removed from the book`,
		"status     canceled at seq 8",
		"seq 5 #0 reservation", "user 3 available BTC -0.1000; user 3 reserved BTC +0.1000",
		"seq 7 #3 settlement",
		"seq 8 #0 release", "user 3 reserved BTC -0.0500; user 3 available BTC +0.0500",
	)
}

// TestExplainToSeq: with -to-seq the story stops at that point in time.
func TestExplainToSeq(t *testing.T) {
	dir, ids, _ := explainJournal(t)
	out := explain(t, "-data-dir", dir, "-to-seq", "6", "-explain", fmt.Sprint(ids["young"]))
	mustContainInOrder(t, out, "status     resting on the book, 0.1000 BTC remaining (as of seq 6)")
	if strings.Contains(out, "hit by") {
		t.Error("as of seq 6 nothing had hit the order yet")
	}
}

// TestExplainSeq shows one command, including a duplicate retry.
func TestExplainSeq(t *testing.T) {
	dir, ids, _ := explainJournal(t)
	out := explain(t, "-data-dir", dir, "-explain-seq", "7")
	mustContainInOrder(t, out,
		"seq        7",
		`request    user 1, key "b1": buy 0.2500 BTC limit 60050, GTC`,
		fmt.Sprintf("result     accepted, order %d, filled, 0.0000 BTC remaining", ids["taker"]),
		"fill 1", "fill 2", "fill 3",
		"balances   user 1 before: BTC 0.0000 available",
		"after:  BTC 0.2500 available",
		"ledger     #0 reservation", "ledger     #4 release",
	)
	dup := explain(t, "-data-dir", dir, "-explain-seq", "9")
	mustContainInOrder(t, dup, "result     DUPLICATE of seq 7: the stored result was returned, nothing executed",
		"stored fill 1", "ledger     no postings")
}
