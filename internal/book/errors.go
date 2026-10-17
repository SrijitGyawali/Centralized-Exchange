package book

import "errors"

// Sentinel errors returned by the book. Callers check them with errors.Is.
// The returned errors wrap these with details (which order, which field), and
// unit problems also wrap domain.ErrOutOfRange, so both checks work:
//
//	errors.Is(err, book.ErrInvalidOrder)    // true
//	errors.Is(err, domain.ErrOutOfRange)    // also true for a bad price or qty
var (
	// ErrInvalidOrder: a field is missing (zero ID, user or side) or outside
	// its allowed range (price, quantity).
	ErrInvalidOrder = errors.New("invalid order")

	// ErrDuplicateOrder: an order with this ID is already resting.
	ErrDuplicateOrder = errors.New("duplicate order id")

	// ErrUnknownOrder: no resting order has this ID. This is also what a
	// second cancel of the same order gets, so funds can never be released
	// twice.
	ErrUnknownOrder = errors.New("unknown order id")

	// ErrSeqNotIncreasing: the order's Seq is not higher than every Seq the
	// book has seen. FIFO priority is only correct if orders arrive in
	// sequence order, so the book enforces it instead of trusting callers.
	ErrSeqNotIncreasing = errors.New("sequence not increasing")

	// ErrWouldCross: the order would trade immediately (a buy at or above the
	// best ask, or a sell at or below the best bid). Matching arrives in week
	// 2; until then, resting such an order would leave a "crossed" book,
	// which must never exist.
	ErrWouldCross = errors.New("order would cross the book")
)
