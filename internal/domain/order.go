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
