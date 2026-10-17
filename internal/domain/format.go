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
//
// Like parsing, formatting never goes through float64: the whole and
// fractional parts come from integer division and remainder.
func FormatQty(q Qty) string {
	sign := ""
	u := uint64(q)
	if q < 0 {
		sign = "-"
		// For MinInt64, -q wraps back to MinInt64, but converting that to
		// uint64 still gives the correct magnitude (2^63). Unsigned
		// conversion is how Go code takes |x| without overflow.
		u = uint64(-q)
	}
	return fmt.Sprintf("%s%d.%0*d", sign, u/LotsPerBTC, QtyDecimals, u%LotsPerBTC)
}

// FormatPrice renders ticks as a USDT-per-BTC string. Prices are whole
// numbers (1 tick = 1 USDT), so this is plain integer formatting:
// FormatPrice(60000) = "60000".
func FormatPrice(p Price) string {
	return strconv.FormatInt(int64(p), 10)
}
