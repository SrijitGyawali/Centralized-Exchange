package book

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// Order is a resting limit order as the book sees it.
//
// The book stores its own copy and only ever hands out copies (Get, Cancel,
// QueueAt). Callers can never change a resting order behind the book's back,
// because Order holds plain values and no pointers.
type Order struct {
	ID    domain.OrderID
	User  domain.UserID
	Side  domain.Side
	Price domain.Price // limit price, in ticks
	Qty   domain.Qty   // remaining quantity, in lots
	Seq   domain.Seq   // admission sequence: lower Seq = earlier in the queue
}

// validate checks every field. Book.Add calls it before changing anything.
//
// The %w verb appears twice for unit errors (allowed since Go 1.20), so the
// result matches both ErrInvalidOrder and domain.ErrOutOfRange in errors.Is.
func (o Order) validate() error {
	switch {
	case o.ID == 0:
		return fmt.Errorf("%w: order id must be non-zero", ErrInvalidOrder)
	case o.User == 0:
		return fmt.Errorf("%w: user id must be non-zero", ErrInvalidOrder)
	case !o.Side.Valid():
		return fmt.Errorf("%w: side %v", ErrInvalidOrder, o.Side)
	case o.Price < domain.MinPrice || o.Price > domain.MaxPrice:
		return fmt.Errorf("%w: price %d: %w", ErrInvalidOrder, o.Price, domain.ErrOutOfRange)
	case o.Qty < domain.MinQty || o.Qty > domain.MaxQty:
		return fmt.Errorf("%w: qty %d: %w", ErrInvalidOrder, o.Qty, domain.ErrOutOfRange)
	}
	return nil
}
