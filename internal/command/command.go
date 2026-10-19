package command

import "github.com/SrijitGyawali/Centralized-Exchange/internal/domain"

// MaxKeyLen bounds the idempotency key. Every accepted key is remembered for
// deduplication, so an unbounded key would let a client grow server memory
// at will.
const MaxKeyLen = 64

// Command is one instruction to change exchange state: who sent it, the
// client's idempotency key, and what to do.
//
// It is plain data, with no channels, functions or pointers into live state,
// because it will be written to the journal and replayed after a crash. The
// engine keeps each request's reply channel NEXT TO its command, never
// inside it.
type Command struct {
	// Seq is assigned by the engine when it sequences the command (0 before
	// that). It is the global order of everything that ever happened.
	Seq domain.Seq
	// User is the authenticated caller.
	User domain.UserID
	// Key is the client's idempotency key, unique per user. Sending the same
	// request again with the same Key returns the original result instead of
	// doing it twice: the "lost response, retry safely" rule.
	Key string
	// Body says what to do.
	Body Body
}

// Body is what a command asks for: PlaceOrder, CancelOrder or Fund.
//
// It is a *sealed* interface: its only method is unexported, so no type
// outside this package can implement it. A type switch over these three
// cases is therefore complete, and the journal can encode every body it
// will ever see.
//
// Every Body type must stay comparable (no slices, maps or funcs inside),
// because deduplication compares a retried request with the original
// using ==.
type Body interface{ isBody() }

// PlaceOrder submits a limit order for the caller. The state assigns the
// order's ID; clients learn it from the result.
type PlaceOrder struct {
	Side  domain.Side
	Price domain.Price // limit, in ticks
	Qty   domain.Qty   // in lots
	TIF   domain.TimeInForce
}

// CancelOrder removes the caller's own resting order and releases its
// reserved funds.
type CancelOrder struct {
	Order domain.OrderID
}

// Fund creates test money for a user: the paper-trading stand-in for a
// deposit. Amount is in the asset's smallest unit (lots or atoms). Only
// operators may send it; the API enforces that (week 5).
type Fund struct {
	To     domain.UserID
	Asset  domain.Asset
	Amount int64
}

func (PlaceOrder) isBody()  {}
func (CancelOrder) isBody() {}
func (Fund) isBody()        {}
