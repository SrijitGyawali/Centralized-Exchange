package command

import (
	"fmt"
	"slices"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// RejectReason says why a command was refused. The zero value, NotRejected,
// means it was accepted.
//
// A business rejection is a normal, deterministic RESULT, not a Go error. It
// is remembered for deduplication and replays identically. Go errors are
// reserved for bugs in the caller, such as an out-of-order Seq.
type RejectReason uint8

const (
	NotRejected RejectReason = iota
	// RejectInvalid: a field is missing or out of range (side, price, qty,
	// time in force, asset, amount, recipient, idempotency key).
	RejectInvalid
	// RejectInsufficientFunds: the user's available balance cannot cover
	// the reservation the order needs.
	RejectInsufficientFunds
	// RejectUnknownOrder: the caller has no open order with that ID. This is
	// deliberately the same answer for "never existed", "already filled or
	// canceled" and "belongs to someone else", so it reveals nothing about
	// other users' orders.
	RejectUnknownOrder
	// RejectKeyConflict: this idempotency key was already used for a
	// DIFFERENT request. The API maps it to HTTP 409.
	RejectKeyConflict
	// RejectSupplyCap: funding would push the asset's total supply past
	// its MaxSupply.
	RejectSupplyCap
)

// String implements fmt.Stringer.
func (r RejectReason) String() string {
	switch r {
	case NotRejected:
		return "accepted"
	case RejectInvalid:
		return "invalid request"
	case RejectInsufficientFunds:
		return "insufficient funds"
	case RejectUnknownOrder:
		return "unknown order"
	case RejectKeyConflict:
		return "idempotency key conflict"
	case RejectSupplyCap:
		return "supply cap reached"
	default:
		return fmt.Sprintf("RejectReason(%d)", uint8(r))
	}
}

// Result is the deterministic outcome of one command: what the client is
// told, and what is remembered for deduplication.
type Result struct {
	// Seq is the command that decided this result. For a duplicate it is the
	// ORIGINAL command's Seq, so a retry can be traced to the first attempt.
	Seq domain.Seq
	// Rejected is NotRejected for success, otherwise the reason.
	Rejected RejectReason
	// Detail explains a rejection in words, e.g. "needs 6000.000000 USDT,
	// available 100.000000".
	Detail string
	// Duplicate is true when this is a stored result returned for a retried
	// key. Nothing was executed again.
	Duplicate bool

	// PlaceOrder results:
	Order     domain.OrderID // the ID assigned to the new order
	Outcome   book.Outcome   // filled, rested, or why the remainder was canceled
	Fills     []book.Trade   // in execution order
	Remaining domain.Qty     // quantity that did not trade

	// CancelOrder results:
	Canceled domain.Qty // quantity removed from the book
}

// Accepted reports whether the command was carried out.
func (r Result) Accepted() bool { return r.Rejected == NotRejected }

// Clone returns a deep copy.
//
// Result holds a slice. Copying the struct copies only the slice HEADER,
// so both copies would share one backing array, and a caller appending to or
// editing its Fills could change the stored dedupe result. Clone gives the
// copy its own array. Go concept: slices are references to shared memory.
func (r Result) Clone() Result {
	r.Fills = slices.Clone(r.Fills)
	return r
}
