package book

import (
	"fmt"
	"strings"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// LevelSummary is one row of market depth: a price, the total quantity
// resting there, and how many orders make up that quantity.
type LevelSummary struct {
	Price  domain.Price
	Qty    domain.Qty
	Orders int
}

// Depth is a snapshot of the book, best price first on each side.
//
// It is a deep copy: freshly allocated slices of plain values with no
// pointers into the book. Changing a Depth cannot change the book, and later
// changes to the book cannot change an existing Depth. That is what will make
// it safe to hand to other goroutines (market data, HTTP) while the engine
// keeps changing the book (ADR-0001: readers get immutable copies).
type Depth struct {
	Bids []LevelSummary
	Asks []LevelSummary
}

// Depth returns up to n levels per side. n <= 0 means every level.
func (b *Book) Depth(n int) Depth {
	return Depth{
		Bids: summarize(b.bids.levels, n),
		Asks: summarize(b.asks.levels, n),
	}
}

// summarize copies the first n levels into a NEW slice.
//
// Returning something built from b.bids.levels[:n] would be a classic Go
// bug. A sub-slice shares the original's backing array, so the caller would
// be looking straight into the book's live memory. make() allocates a
// separate array, so the result is an independent copy.
func summarize(levels []*level, n int) []LevelSummary {
	if n <= 0 || n > len(levels) {
		n = len(levels)
	}
	out := make([]LevelSummary, n)
	for i, l := range levels[:n] {
		out[i] = LevelSummary{Price: l.price, Qty: l.total, Orders: l.orders}
	}
	return out
}

// String renders the depth as a price ladder the way trading screens show
// it: asks on top (highest price first, so the best ask sits just above the
// spread), then the spread, then bids (best first).
//
//	ASK    60200    0.1500   1
//	ASK    60100    0.3000   1
//	----- spread 100 -----
//	BID    60000    0.3500   2
//	BID    59900    0.5000   1
//
// Columns: side, price (USDT/BTC), total quantity (BTC), number of orders.
// Depth implements fmt.Stringer, so fmt.Print(depth) uses this.
func (d Depth) String() string {
	var sb strings.Builder
	for i := len(d.Asks) - 1; i >= 0; i-- {
		writeRow(&sb, "ASK", d.Asks[i])
	}
	if len(d.Bids) > 0 && len(d.Asks) > 0 {
		fmt.Fprintf(&sb, "----- spread %d -----\n", d.Asks[0].Price-d.Bids[0].Price)
	} else {
		sb.WriteString("----- no spread (a side is empty) -----\n")
	}
	for _, row := range d.Bids {
		writeRow(&sb, "BID", row)
	}
	return sb.String()
}

func writeRow(sb *strings.Builder, label string, r LevelSummary) {
	fmt.Fprintf(sb, "%s %8s %9s %3d\n", label, domain.FormatPrice(r.Price), domain.FormatQty(r.Qty), r.Orders)
}
