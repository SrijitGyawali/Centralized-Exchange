package domain

import (
	"errors"
	"math/big"
	"testing"
)

func TestParseAmount(t *testing.T) {
	for _, tc := range []struct {
		asset Asset
		input string
		want  int64
		err   error
	}{
		{BTC, "0.1", 1000, nil}, {USDT, "0.1", 100000, nil},
		{BTC, "11", 110000, nil}, {BTC, "21000000", BTC.MaxSupply(), nil},
		{USDT, "1000000000", USDT.MaxSupply(), nil},
		{USDT, "0.000001", 1, nil}, {BTC, "0.0001", 1, nil},
		{BTC, "21000000.0001", 0, ErrOutOfRange},
		{USDT, "1000000000.000001", 0, ErrOutOfRange},
		{BTC, "0", 0, ErrOutOfRange}, {USDT, "0", 0, ErrOutOfRange},
		{BTC, "0.00001", 0, ErrPrecision}, {USDT, "0.0000001", 0, ErrPrecision},
		{Asset(0), "1", 0, ErrOutOfRange}, {BTC, "1e2", 0, ErrSyntax},
		{USDT, "-1", 0, ErrSyntax}, {USDT, "01", 0, ErrSyntax},
		{USDT, "99999999999999999999", 0, ErrOutOfRange},
	} {
		t.Run(tc.asset.String()+"/"+tc.input, func(t *testing.T) {
			got, err := ParseAmount(tc.asset, tc.input)
			if got != tc.want || !errors.Is(err, tc.err) {
				t.Fatalf("got %d, %v; want %d, %v", got, err, tc.want, tc.err)
			}
		})
	}
}

// TestParseQty is table-driven: every row is one input and its expected
// result. Error rows name the sentinel error expected, and errors.Is checks it,
// so the test checks the *kind* of failure and not the exact message text.
func TestParseQty(t *testing.T) {
	tests := []struct {
		in      string
		want    Qty
		wantErr error
	}{
		// Valid quantities (1 lot = 0.0001 BTC).
		{in: "0.1", want: 1_000},
		{in: "10", want: 100_000}, // MaxQty exactly
		{in: "0.0001", want: 1},   // MinQty exactly
		{in: "1", want: 10_000},
		{in: "0.25", want: 2_500},
		{in: "1.2345", want: 12_345},
		{in: "0.1000", want: 1_000}, // trailing zeros fine within 4 decimals

		// Empty.
		{in: "", wantErr: ErrEmpty},

		// Out of range: zero, and above 10 BTC.
		{in: "0", wantErr: ErrOutOfRange},
		{in: "0.0000", wantErr: ErrOutOfRange},
		{in: "10.0001", wantErr: ErrOutOfRange},
		{in: "11", wantErr: ErrOutOfRange},
		{in: "99999999999999999999999", wantErr: ErrOutOfRange}, // would overflow int64

		// Too precise: a fifth decimal is below one lot.
		{in: "0.00001", wantErr: ErrPrecision},
		{in: "0.10000", wantErr: ErrPrecision}, // strict: digit count, even zeros

		// Syntax: only plain "digits[.digits]" is accepted.
		{in: "-1", wantErr: ErrSyntax},
		{in: "+1", wantErr: ErrSyntax},
		{in: "1e5", wantErr: ErrSyntax},
		{in: " 1", wantErr: ErrSyntax},
		{in: "1 ", wantErr: ErrSyntax},
		{in: ".5", wantErr: ErrSyntax},
		{in: "5.", wantErr: ErrSyntax},
		{in: ".", wantErr: ErrSyntax},
		{in: "1.2.3", wantErr: ErrSyntax},
		{in: "01", wantErr: ErrSyntax},
		{in: "00.1", wantErr: ErrSyntax},
		{in: "1,5", wantErr: ErrSyntax},
		{in: "1_000", wantErr: ErrSyntax},
		{in: "0x10", wantErr: ErrSyntax},
		{in: "NaN", wantErr: ErrSyntax},
		{in: "١", wantErr: ErrSyntax}, // Arabic-Indic digit one: not ASCII
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseQty(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParseQty(%q) error = %v, want %v", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseQty(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParseQty(%q) = %d lots, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestParsePrice(t *testing.T) {
	tests := []struct {
		in      string
		want    Price
		wantErr error
	}{
		{in: "60000", want: 60_000},
		{in: "1", want: 1},               // MinPrice
		{in: "1000000", want: 1_000_000}, // MaxPrice
		{in: "", wantErr: ErrEmpty},
		{in: "0", wantErr: ErrOutOfRange}, // below MinPrice
		{in: "1000001", wantErr: ErrOutOfRange},
		{in: "60000.5", wantErr: ErrPrecision}, // tick is 1 USDT: no decimals
		{in: "60000.0", wantErr: ErrPrecision}, // strict even for ".0"
		{in: "-60000", wantErr: ErrSyntax},
		{in: "6e4", wantErr: ErrSyntax},
		{in: "060000", wantErr: ErrSyntax},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParsePrice(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("ParsePrice(%q) error = %v, want %v", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePrice(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ParsePrice(%q) = %d ticks, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// FuzzParseQty feeds random strings to ParseQty. Plain `go test` runs only the
// seed inputs below. `go test -fuzz=FuzzParseQty ./internal/domain` generates
// new inputs until stopped. Any failing input is saved under
// testdata/fuzz/ and becomes a permanent regression test.
//
// It checks three properties for every input:
//  1. ParseQty never panics.
//  2. A successful result is always inside [MinQty, MaxQty].
//  3. A successful result equals an independent reference: math/big's exact
//     rational parser, multiplied by LotsPerBTC. Comparing two independent
//     implementations is called differential testing.
func FuzzParseQty(f *testing.F) {
	for _, seed := range []string{"0.1", "10", "0.0001", "", "0", "-1", "1e5", "0.00001", " 1", "10.0001", "01", "1.2.3"} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, s string) {
		q, err := ParseQty(s) // property 1: reaching the next line means no panic
		if err != nil {
			return
		}
		if q < MinQty || q > MaxQty {
			t.Fatalf("ParseQty(%q) = %d, outside [%d, %d]", s, q, MinQty, MaxQty)
		}

		ref, ok := new(big.Rat).SetString(s)
		if !ok {
			t.Fatalf("ParseQty accepted %q but big.Rat rejects it", s)
		}
		ref.Mul(ref, big.NewRat(LotsPerBTC, 1))
		if !ref.IsInt() || ref.Num().Int64() != int64(q) {
			t.Fatalf("ParseQty(%q) = %d lots, reference says %s", s, q, ref.RatString())
		}
	})
}
