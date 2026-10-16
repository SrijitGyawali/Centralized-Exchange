package domain

import "errors"

// The three unit types. Each is an int64 count of the smallest unit, so all
// arithmetic is exact (see docs/adr/0002-units-and-fixed-point.md).
//
// They are distinct named types, not aliases: the compiler rejects
// `price + qty` or passing a Qty where a Price is expected. Converting between
// them requires an explicit, visible conversion such as int64(p).

// Price is a limit price in ticks. 1 tick = 1 USDT per BTC.
//
//	60,000 USDT/BTC = Price(60000)
type Price int64

// Qty is a base-asset quantity in lots. 1 lot = 0.0001 BTC.
//
//	0.1 BTC = Qty(1000)
type Qty int64

// Atoms is an amount of quote currency. 1 atom = 0.000001 USDT.
//
//	6,000 USDT = Atoms(6_000_000_000)
type Atoms int64

// Unit constants for the BTC/USDT simulator (ADR-0002).
const (
	// QtyDecimals is how many decimal places a quantity string may have.
	// 4 decimals because 1 lot = 0.0001 BTC.
	QtyDecimals = 4

	// PriceDecimals is how many decimal places a price string may have.
	// 0 because 1 tick = 1 USDT: prices are whole numbers.
	PriceDecimals = 0

	// LotsPerBTC converts whole BTC to lots: 1 BTC = 10,000 lots.
	LotsPerBTC = 10_000

	// AtomsPerUSDT converts whole USDT to atoms: 1 USDT = 1,000,000 atoms.
	AtomsPerUSDT = 1_000_000

	// AtomsPerTickLot is the quote value of 1 tick × 1 lot:
	// 1 USDT/BTC × 0.0001 BTC = 0.0001 USDT = 100 atoms.
	// It is the multiplier in Notional = price × qty × AtomsPerTickLot.
	AtomsPerTickLot Atoms = 100
)

// Bounds on a single order. With these, the largest possible notional is
// 1e6 × 1e5 × 100 = 1e13 atoms, far below MaxInt64 (~9.22e18).
const (
	MinPrice Price = 1
	MaxPrice Price = 1_000_000 // 1,000,000 USDT/BTC

	MinQty Qty = 1       // 0.0001 BTC
	MaxQty Qty = 100_000 // 10 BTC
)

// Sentinel errors. Callers test for them with errors.Is, e.g.
//
//	if errors.Is(err, domain.ErrPrecision) { ... }
//
// The functions in this package wrap them with fmt.Errorf("...: %w", ...) to
// add context (which field, which input) without losing the category.
var (
	// ErrEmpty: the input string was empty.
	ErrEmpty = errors.New("empty value")

	// ErrSyntax: not a plain decimal like "123" or "0.25". Signs, spaces,
	// exponents ("1e5"), leading zeros ("01") and a bare "." are rejected.
	ErrSyntax = errors.New("invalid decimal syntax")

	// ErrPrecision: more decimal places than the unit allows,
	// e.g. "0.00001" BTC when the lot is 0.0001 BTC.
	ErrPrecision = errors.New("too many decimal places")

	// ErrOutOfRange: a syntactically valid number outside the allowed bounds,
	// including zero.
	ErrOutOfRange = errors.New("value out of range")

	// ErrOverflow: an arithmetic result would not fit in int64.
	ErrOverflow = errors.New("arithmetic overflow")
)
