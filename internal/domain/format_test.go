package domain

import (
	"math"
	"testing"
)

func TestFormatQty(t *testing.T) {
	tests := []struct {
		q    Qty
		want string
	}{
		{1, "0.0001"}, // MinQty
		{1_000, "0.1000"},
		{2_500, "0.2500"},
		{10_000, "1.0000"},
		{12_345, "1.2345"},
		{100_000, "10.0000"}, // MaxQty
		{0, "0.0000"},
		{-1, "-0.0001"},
		{math.MinInt64, "-922337203685477.5808"}, // no overflow at the extreme
	}
	for _, tc := range tests {
		if got := FormatQty(tc.q); got != tc.want {
			t.Errorf("FormatQty(%d) = %q, want %q", tc.q, got, tc.want)
		}
	}
}

// TestFormatQtyRoundTrip checks every valid quantity, all 100,000 of them:
// formatting then parsing must give back the original value. When the input
// space is this small, testing all of it beats picking examples.
func TestFormatQtyRoundTrip(t *testing.T) {
	for q := MinQty; q <= MaxQty; q++ {
		s := FormatQty(q)
		got, err := ParseQty(s)
		if err != nil || got != q {
			t.Fatalf("ParseQty(FormatQty(%d)) = %d, %v; want %d (string %q)", q, got, err, q, s)
		}
	}
}

func TestFormatAtoms(t *testing.T) {
	tests := []struct {
		a    Atoms
		want string
	}{
		{1, "0.000001"},
		{100, "0.000100"}, // one tick × one lot
		{1_000_000, "1.000000"},
		{6_000_000_000, "6000.000000"},           // the ADR-0002 example
		{10_000_000_000_000, "10000000.000000"},  // largest possible order
		{-2_500_000, "-2.500000"},                // negative: e.g. the issuance account
		{math.MinInt64, "-9223372036854.775808"}, // no overflow at the extreme
	}
	for _, tc := range tests {
		if got := FormatAtoms(tc.a); got != tc.want {
			t.Errorf("FormatAtoms(%d) = %q, want %q", tc.a, got, tc.want)
		}
	}
}

func TestFormatPrice(t *testing.T) {
	tests := []struct {
		p    Price
		want string
	}{
		{1, "1"},
		{60_000, "60000"},
		{1_000_000, "1000000"},
	}
	for _, tc := range tests {
		if got := FormatPrice(tc.p); got != tc.want {
			t.Errorf("FormatPrice(%d) = %q, want %q", tc.p, got, tc.want)
		}
	}
}
