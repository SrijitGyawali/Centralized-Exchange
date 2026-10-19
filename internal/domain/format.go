package domain

import (
	"fmt"
	"strconv"
)

// FormatQty renders lots as a BTC decimal string with exactly QtyDecimals
// places:
//
//	FormatQty(1000)   = "0.1000"
//	FormatQty(100000) = "10.0000"
//
// It is the inverse of ParseQty: ParseQty(FormatQty(q)) == q for every valid
// quantity, which format_test.go checks for all 100,000 of them. The fixed
// width keeps columns aligned in depth ladders and logs.
func FormatQty(q Qty) string {
	return formatFixed(int64(q), LotsPerBTC)
}

// FormatAtoms renders quote atoms as a USDT decimal string with 6 places:
//
//	FormatAtoms(6_000_000_000) = "6000.000000"
//	FormatAtoms(100)           = "0.000100"
func FormatAtoms(a Atoms) string {
	return formatFixed(int64(a), AtomsPerUSDT)
}

// FormatPrice renders ticks as a USDT-per-BTC string. Prices are whole
// numbers (1 tick = 1 USDT), so this is plain integer formatting:
// FormatPrice(60000) = "60000".
func FormatPrice(p Price) string {
	return strconv.FormatInt(int64(p), 10)
}

// formatFixed renders v smallest-units as a decimal with as many places as
// scale has zeros: scale 10,000 gives 4 places, scale 1,000,000 gives 6.
//
// Like parsing, formatting never goes through float64: the whole and
// fractional parts come from integer division and remainder.
func formatFixed(v int64, scale uint64) string {
	decimals := len(strconv.FormatUint(scale, 10)) - 1
	sign := ""
	u := uint64(v)
	if v < 0 {
		sign = "-"
		// For MinInt64, -v wraps back to MinInt64, but converting that to
		// uint64 still gives the correct magnitude (2^63). Unsigned
		// conversion is how Go code takes |x| without overflow.
		u = uint64(-v)
	}
	return fmt.Sprintf("%s%d.%0*d", sign, u/scale, decimals, u%scale)
}
