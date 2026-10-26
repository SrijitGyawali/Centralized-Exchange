package projector

import (
	"fmt"
	"math"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// stmt is one SQL statement and its arguments.
type stmt struct {
	sql  string
	args []any
}

// plan translates ONE applied command into the SQL statements that record
// it. It is pure: no database, no clock, no I/O. The same command and effects
// always produce the same statements, which is why the projection can be
// rebuilt and compared, and why this function is unit-tested on its own.
//
// What gets written:
//
//	every command      -> commands               (also rejections and duplicates)
//	accepted order     -> orders                 (lifecycle: open, partially_filled, filled, canceled...)
//	every fill         -> trades + maker's order row updated
//	accepted cancel    -> orders row closed
//	every journal      -> ledger_journals + ledger_lines + account_balances (+= amount)
//
// A duplicate (a retried key) is recorded in commands only: its effects were
// recorded when the original ran, and its Effects carry no journals.
func plan(cmd command.Command, fx state.Effects) ([]stmt, error) {
	seq, err := i64(uint64(cmd.Seq))
	if err != nil {
		return nil, err
	}
	user, err := i64(uint64(cmd.User))
	if err != nil {
		return nil, err
	}
	r := fx.Result
	var kind string
	switch cmd.Body.(type) {
	case command.Fund:
		kind = "fund"
	case command.PlaceOrder:
		kind = "place"
	case command.CancelOrder:
		kind = "cancel"
	default:
		return nil, fmt.Errorf("projector: unknown body %T at seq %d", cmd.Body, cmd.Seq)
	}
	rejected := ""
	if !r.Accepted() {
		rejected = r.Rejected.String()
	}
	out := []stmt{{
		`insert into commands (seq, user_id, idem_key, kind, accepted, rejected, duplicate)
		 values ($1, $2, $3, $4, $5, $6, $7)`,
		[]any{seq, user, cmd.Key, kind, r.Accepted(), rejected, r.Duplicate},
	}}
	if r.Duplicate || !r.Accepted() {
		return out, nil
	}

	switch b := cmd.Body.(type) {
	case command.PlaceOrder:
		orderID, err := i64(uint64(r.Order))
		if err != nil {
			return nil, err
		}
		filled := int64(b.Qty - r.Remaining)
		var open int64
		var status string
		switch r.Outcome {
		case book.Filled:
			status = "filled"
		case book.Rested:
			open = int64(r.Remaining)
			status = "open"
			if filled > 0 {
				status = "partially_filled"
			}
		case book.CanceledIOC:
			status = "canceled_ioc"
		case book.CanceledSelfTrade:
			status = "canceled_self_trade"
		default:
			return nil, fmt.Errorf("projector: seq %d: unexpected outcome %v", cmd.Seq, r.Outcome)
		}
		out = append(out, stmt{
			`insert into orders (order_id, user_id, side, time_in_force, price_ticks, qty_lots,
			                     filled_lots, open_lots, status, placed_seq, updated_seq)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $10)`,
			[]any{orderID, user, b.Side.String(), b.TIF.String(), int64(b.Price), int64(b.Qty), filled, open, status, seq},
		})
		for i, f := range r.Fills {
			maker, err := i64(uint64(f.Maker))
			if err != nil {
				return nil, err
			}
			takerUser, err := i64(uint64(f.TakerUser))
			if err != nil {
				return nil, err
			}
			makerUser, err := i64(uint64(f.MakerUser))
			if err != nil {
				return nil, err
			}
			out = append(out,
				stmt{
					`insert into trades (seq, fill_index, taker_order_id, maker_order_id, taker_user_id,
					                     maker_user_id, taker_side, price_ticks, qty_lots)
					 values ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
					[]any{seq, i, orderID, maker, takerUser, makerUser, f.TakerSide.String(), int64(f.Price), int64(f.Qty)},
				},
				stmt{
					// The maker's row: more filled, less open. A partial fill
					// keeps the order alive; a complete one closes it. $2 is
					// used twice, so it is cast explicitly: otherwise
					// PostgreSQL infers bigint in one place and integer in the
					// other and refuses ("inconsistent types deduced").
					`update orders set filled_lots = filled_lots + $1, open_lots = $2::bigint,
					        status = case when $2::bigint = 0 then 'filled' else 'partially_filled' end,
					        updated_seq = $3
					 where order_id = $4`,
					[]any{int64(f.Qty), int64(f.MakerRemaining), seq, maker},
				},
			)
		}
	case command.CancelOrder:
		orderID, err := i64(uint64(b.Order))
		if err != nil {
			return nil, err
		}
		out = append(out, stmt{
			`update orders set open_lots = 0, status = 'canceled', updated_seq = $1 where order_id = $2`,
			[]any{seq, orderID},
		})
	}

	for _, j := range fx.Journals {
		order, err := i64(uint64(j.Order))
		if err != nil {
			return nil, err
		}
		maker, err := i64(uint64(j.Maker))
		if err != nil {
			return nil, err
		}
		out = append(out, stmt{
			`insert into ledger_journals (seq, journal_index, kind, order_id, maker_order_id)
			 values ($1, $2, $3, $4, $5)`,
			[]any{seq, j.Index, j.Kind.String(), order, maker},
		})
		for li, p := range j.Postings {
			owner, err := i64(uint64(p.Account.User))
			if err != nil {
				return nil, err
			}
			out = append(out,
				stmt{
					`insert into ledger_lines (seq, journal_index, line_index, user_id, bucket, asset, amount)
					 values ($1, $2, $3, $4, $5, $6, $7)`,
					[]any{seq, j.Index, li, owner, p.Account.Bucket.String(), p.Asset.String(), p.Amount},
				},
				stmt{
					`insert into account_balances (user_id, asset, bucket, amount) values ($1, $2, $3, $4)
					 on conflict (user_id, asset, bucket) do update set amount = account_balances.amount + excluded.amount`,
					[]any{owner, p.Asset.String(), p.Account.Bucket.String(), p.Amount},
				},
			)
		}
	}
	return out, nil
}

// i64 converts an unsigned identifier to PostgreSQL's signed BIGINT. IDs
// above MaxInt64 cannot be stored faithfully, and silently wrapping them to
// negative numbers would corrupt the projection, so they are an error.
func i64(v uint64) (int64, error) {
	if v > math.MaxInt64 {
		return 0, fmt.Errorf("projector: identifier %d does not fit in BIGINT", v)
	}
	return int64(v), nil
}
