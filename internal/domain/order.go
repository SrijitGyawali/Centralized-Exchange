package domain

import "fmt"

// Side is the direction of an order.
//
// The zero value is deliberately invalid. A Side that was never set (for
// example, a missing JSON field) cannot silently turn into Buy. Code must
// choose Buy or Sell explicitly, and Valid catches anything else.
type Side uint8

const (
	// Buy is a bid: the user wants to buy the base asset (BTC) and pay quote (USDT).
	Buy Side = iota + 1
	// Sell is an ask: the user wants to sell the base asset and receive quote.
	Sell
)

// Valid reports whether s is Buy or Sell.
func (s Side) Valid() bool { return s == Buy || s == Sell }

// String implements fmt.Stringer, so fmt.Println(domain.Buy) prints "buy"
// and error messages read naturally.
func (s Side) String() string {
	switch s {
	case Buy:
		return "buy"
	case Sell:
		return "sell"
	default:
		return fmt.Sprintf("Side(%d)", uint8(s))
	}
}

// Opposite returns the side an order trades against: a buy matches sells,
// a sell matches buys. An invalid side stays invalid.
func (s Side) Opposite() Side {
	switch s {
	case Buy:
		return Sell
	case Sell:
		return Buy
	default:
		return s
	}
}

// TimeInForce says what happens to the part of an order that does not
// trade immediately.
//
// Like Side, the zero value is invalid, so a forgotten field is rejected
// instead of silently defaulting to one behaviour.
//
// FOK (fill-or-kill) is deliberately absent. It needs a side-effect-free
// feasibility check using the same liquidity and self-trade rules as
// execution, which the plan defers until the basics are solid.
type TimeInForce uint8

const (
	// GTC (good 'til canceled): match what you can, then rest the remainder
	// in the book until it fills or is canceled.
	GTC TimeInForce = iota + 1
	// IOC (immediate or cancel): match what you can, then cancel the
	// remainder. An IOC order never rests.
	IOC
)

// Valid reports whether t is GTC or IOC.
func (t TimeInForce) Valid() bool { return t == GTC || t == IOC }

// String implements fmt.Stringer: "gtc", "ioc".
func (t TimeInForce) String() string {
	switch t {
	case GTC:
		return "gtc"
	case IOC:
		return "ioc"
	default:
		return fmt.Sprintf("TimeInForce(%d)", uint8(t))
	}
}

// OrderID identifies an order. The engine assigns IDs from 1 upward, so the
// zero value means "no order".
type OrderID uint64

// UserID identifies an account holder. The zero value means "no user".
type UserID uint64

// Seq is the global command sequence number the engine assigns to every
// command (ADR-0001). It defines both replay order and time priority: at
// the same price, the order with the lower Seq is first in the queue. The
// first command is Seq 1.
type Seq uint64
