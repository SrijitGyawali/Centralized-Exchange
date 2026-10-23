package domain

import (
	"fmt"
	"math"
	"strings"
)

// ParseAmount parses positive test funding in the asset's smallest unit.
// Unlike ParseQty, funding is bounded by the asset supply, not order size.
func ParseAmount(asset Asset, s string) (int64, error) {
	decimals := 6 // USDT atoms
	if !asset.Valid() {
		return 0, fmt.Errorf("asset: %w", ErrOutOfRange)
	}
	if asset == BTC {
		decimals = QtyDecimals
	}
	v, err := parseFixed(s, decimals)
	if err != nil {
		return 0, fmt.Errorf("%s amount: %w", asset, err)
	}
	if v <= 0 || v > asset.MaxSupply() {
		return 0, fmt.Errorf("%s amount: %w", asset, ErrOutOfRange)
	}
	return v, nil
}

// ParseQty converts a decimal BTC string such as "0.1" into lots.
//
//	ParseQty("0.1")    → 1000
//	ParseQty("10")     → 100000 (MaxQty)
//	ParseQty("0.0001") → 1      (MinQty)
//
// Accepted syntax is strict: ASCII digits, optionally followed by "." and up
// to QtyDecimals more digits. No sign, spaces, exponent, separators or
// leading zeros ("01"). The fractional part may have at most 4 digits, even
// if they are zeros ("0.10000" is rejected), so every accepted string maps to
// exactly one value and a client bug is reported instead of silently
// accepted.
//
// "Parse, don't validate": this does not merely check the string, it returns
// a Qty. Code that holds a Qty from here knows it is in range.
func ParseQty(s string) (Qty, error) {
	v, err := parseFixed(s, QtyDecimals)
	if err != nil {
		return 0, fmt.Errorf("quantity %q: %w", s, err)
	}
	q := Qty(v)
	if q < MinQty || q > MaxQty {
		return 0, fmt.Errorf("quantity %q: %w: must be %d..%d lots (0.0001..10 BTC)", s, ErrOutOfRange, MinQty, MaxQty)
	}
	return q, nil
}

// ParsePrice converts a decimal USDT-per-BTC string such as "60000" into
// ticks. The tick is 1 USDT, so prices are whole numbers: "60000.5" and even
// "60000.0" are rejected with ErrPrecision.
func ParsePrice(s string) (Price, error) {
	v, err := parseFixed(s, PriceDecimals)
	if err != nil {
		return 0, fmt.Errorf("price %q: %w", s, err)
	}
	p := Price(v)
	if p < MinPrice || p > MaxPrice {
		return 0, fmt.Errorf("price %q: %w: must be %d..%d", s, ErrOutOfRange, MinPrice, MaxPrice)
	}
	return p, nil
}

// parseFixed parses a non-negative decimal string and returns it scaled by
// 10^decimals as an int64, e.g. parseFixed("0.25", 4) = 2500.
//
// It never uses float64. Floats cannot represent most decimals exactly
// (0.1 + 0.2 != 0.3), so the string is processed one digit at a time with
// integer arithmetic, and every step is checked for overflow.
func parseFixed(s string, decimals int) (int64, error) {
	if s == "" {
		return 0, ErrEmpty
	}

	// strings.Cut splits around the first "." only: "1.2.3" gives
	// intPart "1" and fracPart "2.3", whose second "." fails the digit check.
	intPart, fracPart, hasDot := strings.Cut(s, ".")

	// Shape checks: digits are required on both sides of a dot (".5" and "5."
	// are rejected), and only ASCII 0-9 count as digits.
	if intPart == "" || (hasDot && fracPart == "") {
		return 0, ErrSyntax
	}
	if !isASCIIDigits(intPart) || !isASCIIDigits(fracPart) {
		return 0, ErrSyntax
	}
	// Canonical form: no leading zeros, except the single zero in "0.x".
	if len(intPart) > 1 && intPart[0] == '0' {
		return 0, ErrSyntax
	}
	if len(fracPart) > decimals {
		return 0, ErrPrecision
	}

	// Build the scaled integer: integer digits, then fractional digits, then
	// pad with zeros up to `decimals`. "0.25" with 4 decimals: 0 → 2 → 25 →
	// 250 → 2500.
	var v int64
	var err error
	if v, err = appendDigits(v, intPart); err != nil {
		return 0, err
	}
	if v, err = appendDigits(v, fracPart); err != nil {
		return 0, err
	}
	for i := len(fracPart); i < decimals; i++ {
		if v, err = appendDigit(v, 0); err != nil {
			return 0, err
		}
	}
	return v, nil
}

// isASCIIDigits reports whether every byte of s is '0'..'9'. Ranging over
// bytes (not runes) is deliberate: any non-ASCII character, such as an
// Arabic-Indic digit, starts with a byte >= 0x80 and is rejected.
func isASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// appendDigits appends each decimal digit of s to v.
func appendDigits(v int64, s string) (int64, error) {
	var err error
	for i := 0; i < len(s); i++ {
		if v, err = appendDigit(v, int64(s[i]-'0')); err != nil {
			return 0, err
		}
	}
	return v, nil
}

// appendDigit returns v*10 + d, or ErrOutOfRange if that exceeds MaxInt64.
//
// The check happens *before* the multiplication, because Go integers wrap
// around silently on overflow: MaxInt64 + 1 becomes a large negative number,
// with no panic and no error.
func appendDigit(v, d int64) (int64, error) {
	if v > (math.MaxInt64-d)/10 {
		return 0, ErrOutOfRange
	}
	return v*10 + d, nil
}
