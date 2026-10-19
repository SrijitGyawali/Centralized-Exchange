package domain

import (
	"fmt"
	"strconv"
)

// Asset is a currency the exchange keeps balances in.
//
// Every balance is an int64 count of the asset's smallest unit, the same
// units the order book already uses:
//
//	BTC:  lots  (1 lot  = 0.0001 BTC),   like Qty
//	USDT: atoms (1 atom = 0.000001 USDT), like Atoms
//
// So a trade of Qty q at Price p moves q BTC-units one way and
// Notional(p, q) USDT-units the other, with no conversion step in between.
//
// As with Side, the zero value is deliberately invalid.
type Asset uint8

const (
	// BTC is the base asset of the BTC/USDT pair (what is bought and sold).
	BTC Asset = iota + 1
	// USDT is the quote asset (what prices are expressed in and paid with).
	USDT
)

// Valid reports whether a is BTC or USDT.
func (a Asset) Valid() bool { return a == BTC || a == USDT }

// String implements fmt.Stringer: "BTC", "USDT".
func (a Asset) String() string {
	switch a {
	case BTC:
		return "BTC"
	case USDT:
		return "USDT"
	default:
		return fmt.Sprintf("Asset(%d)", uint8(a))
	}
}

// MaxSupply is the most of this asset that test funding may ever create in
// total, in smallest units: 21 million BTC and 1 billion USDT.
//
// Why a cap: user balances can never be negative, and every asset's balances
// (including the system's issuance account) sum to zero. So no single balance
// can ever exceed the total supply. With supply far below MaxInt64
// (2.1e11 lots, 1e15 atoms, versus 9.2e18), adding to a balance can never
// overflow. That is what lets the state package promise that once an order
// is validated, settling its trades cannot fail halfway through.
func (a Asset) MaxSupply() int64 {
	switch a {
	case BTC:
		return 21_000_000 * LotsPerBTC
	case USDT:
		return 1_000_000_000 * AtomsPerUSDT
	default:
		return 0
	}
}

// Format renders an amount of this asset in whole units:
// BTC.Format(1000) = "0.1000", USDT.Format(6_000_000_000) = "6000.000000".
func (a Asset) Format(amount int64) string {
	switch a {
	case BTC:
		return FormatQty(Qty(amount))
	case USDT:
		return FormatAtoms(Atoms(amount))
	default:
		return strconv.FormatInt(amount, 10)
	}
}
