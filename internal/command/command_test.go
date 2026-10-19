package command

import (
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// TestBodyEquality pins down the property deduplication relies on: two
// bodies compare equal with == exactly when they are the same request. An
// interface comparison checks the dynamic TYPE first and then the value.
func TestBodyEquality(t *testing.T) {
	buy := PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1_000, TIF: domain.GTC}

	tests := []struct {
		name string
		a, b Body
		want bool
	}{
		{"identical order", buy, buy, true},
		{"different qty", buy, PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 2_000, TIF: domain.GTC}, false},
		{"different tif", buy, PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1_000, TIF: domain.IOC}, false},
		{"identical cancel", CancelOrder{Order: 7}, CancelOrder{Order: 7}, true},
		{"different cancel", CancelOrder{Order: 7}, CancelOrder{Order: 8}, false},
		{"identical fund", Fund{To: 1, Asset: domain.USDT, Amount: 5}, Fund{To: 1, Asset: domain.USDT, Amount: 5}, true},
		{"different type", CancelOrder{Order: 1}, Fund{To: 1}, false},
	}
	for _, tc := range tests {
		if got := tc.a == tc.b; got != tc.want {
			t.Errorf("%s: (%#v == %#v) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
	}
}

func TestRejectReason(t *testing.T) {
	if !(Result{}).Accepted() {
		t.Error("the zero Result must count as accepted")
	}
	if (Result{Rejected: RejectInsufficientFunds}).Accepted() {
		t.Error("a rejected Result reported Accepted")
	}
	if got := RejectKeyConflict.String(); got != "idempotency key conflict" {
		t.Errorf("RejectKeyConflict.String() = %q", got)
	}
	if got := RejectReason(99).String(); got != "RejectReason(99)" {
		t.Errorf("unknown reason String() = %q", got)
	}
}

// TestCloneIsDeep shows why Clone exists: after cloning, editing the copy's
// fills must not reach the original's.
func TestCloneIsDeep(t *testing.T) {
	orig := Result{Fills: []book.Trade{{Price: 100, Qty: 1}}}

	shallow := orig        // copies the slice header only
	deep := orig.Clone()   // copies the elements too
	deep.Fills[0].Qty = 99 // must not affect orig
	if orig.Fills[0].Qty != 1 {
		t.Fatalf("Clone shares memory with the original: Qty = %d", orig.Fills[0].Qty)
	}

	shallow.Fills[0].Qty = 42 // a plain copy DOES share it, which is the bug Clone prevents
	if orig.Fills[0].Qty != 42 {
		t.Fatal("expected a plain struct copy to share the Fills array")
	}
}
