package domain

import (
	"math"
	"testing"
)

func TestAsset(t *testing.T) {
	var unset Asset

	tests := []struct {
		asset     Asset
		wantValid bool
		wantStr   string
		wantMax   int64
	}{
		{BTC, true, "BTC", 210_000_000_000},         // 21M BTC in lots
		{USDT, true, "USDT", 1_000_000_000_000_000}, // 1B USDT in atoms
		{unset, false, "Asset(0)", 0},
		{Asset(5), false, "Asset(5)", 0},
	}
	for _, tc := range tests {
		if got := tc.asset.Valid(); got != tc.wantValid {
			t.Errorf("%v.Valid() = %v, want %v", tc.asset, got, tc.wantValid)
		}
		if got := tc.asset.String(); got != tc.wantStr {
			t.Errorf("Asset(%d).String() = %q, want %q", uint8(tc.asset), got, tc.wantStr)
		}
		if got := tc.asset.MaxSupply(); got != tc.wantMax {
			t.Errorf("%v.MaxSupply() = %d, want %d", tc.asset, got, tc.wantMax)
		}
	}
}

// TestMaxSupplyLeavesOverflowHeadroom pins down the property the state
// package relies on: even two maximal balances added together cannot
// overflow int64. If someone raises a cap carelessly, this test fails.
func TestMaxSupplyLeavesOverflowHeadroom(t *testing.T) {
	for _, a := range []Asset{BTC, USDT} {
		if a.MaxSupply() > math.MaxInt64/2 {
			t.Errorf("%v.MaxSupply() = %d leaves no headroom below MaxInt64", a, a.MaxSupply())
		}
	}
}

func TestAssetFormat(t *testing.T) {
	tests := []struct {
		asset  Asset
		amount int64
		want   string
	}{
		{BTC, 1_000, "0.1000"},
		{USDT, 6_000_000_000, "6000.000000"},
		{Asset(9), 42, "42"},
	}
	for _, tc := range tests {
		if got := tc.asset.Format(tc.amount); got != tc.want {
			t.Errorf("%v.Format(%d) = %q, want %q", tc.asset, tc.amount, got, tc.want)
		}
	}
}
