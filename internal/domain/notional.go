package domain

import (
	"fmt"
	"math"
)

// Notional returns the quote value of trading q lots at price p:
//
//	Notional = price (ticks) × qty (lots) × AtomsPerTickLot (100)
//
// Example: 60,000 × 1,000 × 100 = 6,000,000,000 atoms = 6,000 USDT.
//
// It returns (value, error) instead of a bare value because Go integer
// arithmetic wraps silently on overflow. An exchange must refuse a trade it
// cannot price exactly rather than settle it at a garbage amount. Inputs from
// ParsePrice/ParseQty can never overflow (max 1e13 atoms), but Notional
// does not rely on its callers: it checks every multiplication itself.
func Notional(p Price, q Qty) (Atoms, error) {
	if p <= 0 || q <= 0 {
		return 0, fmt.Errorf("notional of price %d × qty %d: %w: both must be positive", p, q, ErrOutOfRange)
	}
	pq, err := mulPositive(int64(p), int64(q))
	if err != nil {
		return 0, fmt.Errorf("notional of price %d × qty %d: %w", p, q, err)
	}
	n, err := mulPositive(pq, int64(AtomsPerTickLot))
	if err != nil {
		return 0, fmt.Errorf("notional of price %d × qty %d: %w", p, q, err)
	}
	return Atoms(n), nil
}

// mulPositive returns a × b for positive a and b, or ErrOverflow if the
// product does not fit in int64.
//
// The test `a > MaxInt64 / b` is the overflow check from ADR-0002. It decides
// *before* multiplying, using a division that cannot itself overflow.
// Multiplying first and checking afterwards would be too late, because the
// wrapped result may look like a perfectly ordinary number.
func mulPositive(a, b int64) (int64, error) {
	if a > math.MaxInt64/b {
		return 0, ErrOverflow
	}
	return a * b, nil
}
