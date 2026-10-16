package domain

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

func TestNotional(t *testing.T) {
	tests := []struct {
		name    string
		p       Price
		q       Qty
		want    Atoms
		wantErr error
	}{
		// ADR-0002 worked example: 0.1 BTC at 60,000 = 6,000 USDT.
		{name: "adr example", p: 60_000, q: 1_000, want: 6_000_000_000},
		// 0.25 BTC at 61,500 = 15,375 USDT.
		{name: "quarter btc", p: 61_500, q: 2_500, want: 15_375_000_000},
		// Smallest possible order: 1 tick × 1 lot = 0.0001 USDT.
		{name: "min order", p: MinPrice, q: MinQty, want: 100},
		// Largest possible order: 1e6 × 1e5 × 100 = 1e13 atoms = 10,000,000 USDT.
		{name: "max order", p: MaxPrice, q: MaxQty, want: 10_000_000_000_000},

		// Non-positive inputs are rejected, never multiplied.
		{name: "zero price", p: 0, q: 1, wantErr: ErrOutOfRange},
		{name: "zero qty", p: 1, q: 0, wantErr: ErrOutOfRange},
		{name: "negative price", p: -1, q: 1, wantErr: ErrOutOfRange},
		{name: "negative qty", p: 1, q: -1, wantErr: ErrOutOfRange},

		// Overflow is detected *before* it happens, in either multiplication.
		{name: "price×qty overflows", p: math.MaxInt64, q: 2, wantErr: ErrOverflow},
		{name: "×100 overflows", p: math.MaxInt64 / 50, q: 1, wantErr: ErrOverflow},
		{name: "largest that fits", p: math.MaxInt64 / 100, q: 1, want: Atoms(math.MaxInt64 / 100 * 100)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Notional(tc.p, tc.q)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Notional(%d, %d) error = %v, want %v", tc.p, tc.q, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Notional(%d, %d) unexpected error: %v", tc.p, tc.q, err)
			}
			if got != tc.want {
				t.Errorf("Notional(%d, %d) = %d atoms, want %d", tc.p, tc.q, got, tc.want)
			}
		})
	}
}

// ExampleNotional is an "example test". It appears in `go doc` as usage
// documentation, and `go test` runs it and compares stdout with the
// "Output:" comment, so the documentation can never go stale.
func ExampleNotional() {
	price, _ := ParsePrice("60000") // 60,000 USDT per BTC
	qty, _ := ParseQty("0.1")       // 0.1 BTC

	n, err := Notional(price, qty)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println(n, "atoms =", int64(n)/AtomsPerUSDT, "USDT")
	// Output: 6000000000 atoms = 6000 USDT
}
