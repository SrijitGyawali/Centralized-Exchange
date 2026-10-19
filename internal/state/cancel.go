package state

import (
	"fmt"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// cancel removes the caller's own resting order and releases everything it
// still has reserved.
//
//	Release journal:  user reserved   -amount
//	                  user available  +amount
//
// An order that does not exist, is already filled or canceled, or belongs to
// someone else gets the SAME rejection (RejectUnknownOrder). Distinct answers
// would let anyone probe which order IDs other users hold.
//
// A second cancel of the same order finds nothing to cancel, so reserved
// funds are released at most once, even before deduplication is considered.
func (s *State) cancel(t *tx, user domain.UserID, c command.CancelOrder) command.Result {
	o, ok := s.open[c.Order]
	if !ok || o.user != user {
		return reject(command.RejectUnknownOrder, "no open order %d for this user", c.Order)
	}

	removed, err := s.book.Cancel(c.Order)
	if err != nil {
		panic(fmt.Sprintf("state: open order %d missing from the book: %v", c.Order, err))
	}
	delete(s.open, c.Order)
	asset, _ := reserveFor(o.side, o.price, removed.Qty)
	t.post(Release, c.Order, 0, move(o.user, asset, Reserved, Available, o.reserved)...)

	return command.Result{Order: c.Order, Canceled: removed.Qty}
}
