package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// rules names the matching rules this replay applies, so an explanation
// says which semantics produced it. They are the Tier 1 rules of
// internal/book (see the comment on book.Place): if they ever change, this
// line and the journal format version change with them.
const rules = "Tier 1, journal format v1: price-time priority (best price, then lowest Seq), " +
	"fills at the maker's price, self-trade stops matching and cancels the taker's remainder"

// tracer watches every command of a replay and keeps what concerns one
// order (-explain) or one command (-explain-seq). Replay is the source of
// truth: everything here is recomputed from the journal by the same
// state.Apply the server ran, nothing is read from logs or SQL.
type tracer struct {
	order domain.OrderID // the order to explain, or 0
	seq   domain.Seq     // the command to explain, or 0

	placedAt map[domain.OrderID]domain.Seq // when each order was accepted: shows time priority

	// -explain
	placed   *step  // the command that created the order
	later    []step // commands that touched it afterwards (hit as a maker, canceled)
	journals []state.Journal

	// -explain-seq
	command *step
}

// step is one command and everything it did, plus the user's balances
// around it.
type step struct {
	cmd           command.Command
	fx            state.Effects
	before, after balances
}

// balances is one user's available and reserved amount of both assets.
type balances struct {
	btcAvail, btcRes, usdtAvail, usdtRes int64
}

func snapshot(st *state.State, user domain.UserID) balances {
	var b balances
	b.btcAvail, b.btcRes = st.Balance(user, domain.BTC)
	b.usdtAvail, b.usdtRes = st.Balance(user, domain.USDT)
	return b
}

func newTracer(order, seq uint64) *tracer {
	return &tracer{order: domain.OrderID(order), seq: domain.Seq(seq), placedAt: map[domain.OrderID]domain.Seq{}}
}

// apply runs one command through the state and records what the
// explanation needs. It is the replay's per-command callback.
func (t *tracer) apply(st *state.State, c command.Command) error {
	before := snapshot(st, c.User)
	fx, err := st.Apply(c)
	if err != nil {
		return err
	}
	s := step{cmd: c, fx: fx, before: before, after: snapshot(st, c.User)}
	r := fx.Result

	if _, ok := c.Body.(command.PlaceOrder); ok && r.Accepted() && !r.Duplicate {
		t.placedAt[r.Order] = c.Seq
		if r.Order == t.order && t.order != 0 {
			t.placed = &s
		}
	}
	if t.seq != 0 && c.Seq == t.seq {
		t.command = &s
	}
	if t.order == 0 || t.placed == nil {
		return nil
	}
	if c.Seq != t.placed.cmd.Seq && !r.Duplicate { // a retry changes nothing: it is not part of the story
		touched := false
		for _, f := range r.Fills {
			touched = touched || f.Maker == t.order
		}
		if cb, ok := c.Body.(command.CancelOrder); ok && cb.Order == t.order && r.Accepted() {
			touched = true
		}
		if touched {
			t.later = append(t.later, s)
		}
	}
	for _, j := range fx.Journals {
		if j.Order == t.order || j.Maker == t.order {
			t.journals = append(t.journals, j)
		}
	}
	return nil
}

// explainOrder prints the life of one order, from request to ledger.
func (t *tracer) explainOrder(out io.Writer, st *state.State) error {
	p := t.placed
	if p == nil {
		return fmt.Errorf("order %d was never accepted in this journal (up to seq %d)", t.order, st.LastSeq())
	}
	body := p.cmd.Body.(command.PlaceOrder)
	r := p.fx.Result
	w := func(format string, a ...any) { fmt.Fprintf(out, format, a...) }

	w("order      %d (BTC-USDT)\n", t.order)
	w("rules      %s\n", rules)
	w("request    seq %d, user %d, key %q: %s\n", p.cmd.Seq, p.cmd.User, p.cmd.Key, describe(body))
	asset, amount := reserveOf(body)
	w("reserve    %s %s at the limit price\n", asset.Format(amount), asset)
	w("           before: %s\n           after:  %s\n", p.before.show(), p.after.show())

	if len(r.Fills) == 0 {
		w("matching   no opposite order at or better than %s: nothing traded\n", domain.FormatPrice(body.Price))
	} else {
		w("matching   %d fill(s), in priority order (best price first; at one price, the oldest order first):\n", len(r.Fills))
		for i, f := range r.Fills {
			w("  %d. %s BTC @ %s against order %d (user %d, resting since seq %d)%s\n",
				i+1, domain.FormatQty(f.Qty), domain.FormatPrice(f.Price), f.Maker, f.MakerUser, t.placedAt[f.Maker], improvement(body, f.Price))
		}
	}
	w("outcome    %s; %s BTC remaining\n", r.Outcome, domain.FormatQty(r.Remaining))

	if len(t.later) > 0 {
		w("later\n")
		for _, s := range t.later {
			if cb, ok := s.cmd.Body.(command.CancelOrder); ok && cb.Order == t.order {
				w("  seq %d  canceled by user %d (key %q): %s BTC removed from the book\n", s.cmd.Seq, s.cmd.User, s.cmd.Key, domain.FormatQty(s.fx.Result.Canceled))
				continue
			}
			for _, f := range s.fx.Result.Fills {
				if f.Maker == t.order {
					w("  seq %d  hit by order %d (user %d, %s): %s BTC @ %s, %s BTC left\n", s.cmd.Seq, f.Taker, f.TakerUser, f.TakerSide,
						domain.FormatQty(f.Qty), domain.FormatPrice(f.Price), domain.FormatQty(f.MakerRemaining))
				}
			}
		}
	}

	w("status     %s\n", t.finalStatus(st))
	w("ledger     every posting about order %d (each journal sums to zero per asset)\n", t.order)
	for _, j := range t.journals {
		w("  seq %d #%d %-11s %s\n", j.Seq, j.Index, j.Kind, postings(j))
	}
	return nil
}

// finalStatus says where the order ended up, as of the replayed Seq.
func (t *tracer) finalStatus(st *state.State) string {
	if o, ok := st.Order(t.order); ok {
		return fmt.Sprintf("resting on the book, %s BTC remaining (as of seq %d)", domain.FormatQty(o.Qty), st.LastSeq())
	}
	for _, s := range t.later {
		if cb, ok := s.cmd.Body.(command.CancelOrder); ok && cb.Order == t.order {
			return fmt.Sprintf("canceled at seq %d", s.cmd.Seq)
		}
	}
	switch o := t.placed.fx.Result.Outcome; o {
	case book.CanceledIOC, book.CanceledSelfTrade:
		return "done at placement: " + o.String()
	}
	return "filled completely"
}

// explainSeq prints one command: what was asked, what was decided, and
// every ledger posting it made.
func (t *tracer) explainSeq(out io.Writer) error {
	s := t.command
	if s == nil {
		return fmt.Errorf("seq %d is not in this journal", t.seq)
	}
	r := s.fx.Result
	w := func(format string, a ...any) { fmt.Fprintf(out, format, a...) }
	w("seq        %d\n", s.cmd.Seq)
	w("request    user %d, key %q: %s\n", s.cmd.User, s.cmd.Key, describe(s.cmd.Body))
	switch {
	case r.Duplicate:
		w("result     DUPLICATE of seq %d: the stored result was returned, nothing executed\n", r.Seq)
	case !r.Accepted():
		w("result     rejected: %s (%s)\n", r.Rejected, r.Detail)
	default:
		w("result     accepted")
		if r.Order != 0 {
			w(", order %d, %s, %s BTC remaining", r.Order, r.Outcome, domain.FormatQty(r.Remaining))
		}
		if r.Canceled != 0 {
			w(", %s BTC canceled", domain.FormatQty(r.Canceled))
		}
		w("\n")
	}
	label := "fill" // a duplicate shows the ORIGINAL fills, which did not happen again
	if r.Duplicate {
		label = "stored fill"
	}
	for i, f := range r.Fills {
		w("  %s %d   %s BTC @ %s, maker order %d (user %d)\n", label, i+1, domain.FormatQty(f.Qty), domain.FormatPrice(f.Price), f.Maker, f.MakerUser)
	}
	w("balances   user %d before: %s\n           after:  %s\n", s.cmd.User, s.before.show(), s.after.show())
	if len(s.fx.Journals) == 0 {
		w("ledger     no postings\n")
	}
	for _, j := range s.fx.Journals {
		w("ledger     #%d %-11s %s\n", j.Index, j.Kind, postings(j))
	}
	return nil
}

// describe renders a command body as a sentence.
func describe(b command.Body) string {
	switch v := b.(type) {
	case command.PlaceOrder:
		return fmt.Sprintf("%s %s BTC limit %s, %s", v.Side, domain.FormatQty(v.Qty), domain.FormatPrice(v.Price), strings.ToUpper(v.TIF.String()))
	case command.CancelOrder:
		return fmt.Sprintf("cancel order %d", v.Order)
	case command.Fund:
		return fmt.Sprintf("fund user %d with %s %s", v.To, v.Asset.Format(v.Amount), v.Asset)
	}
	return fmt.Sprintf("%T", b)
}

// reserveOf is what placing the order holds: USDT at the limit price for a
// buy, the BTC itself for a sell (ADR-0004).
func reserveOf(p command.PlaceOrder) (domain.Asset, int64) {
	if p.Side == domain.Sell {
		return domain.BTC, int64(p.Qty)
	}
	return domain.USDT, int64(p.Price) * int64(p.Qty) * int64(domain.AtomsPerTickLot)
}

// improvement notes when a fill was better than the taker's limit.
func improvement(p command.PlaceOrder, price domain.Price) string {
	if price == p.Price {
		return ""
	}
	return fmt.Sprintf(", %s USDT better than the limit", domain.FormatPrice(max(price-p.Price, p.Price-price)))
}

func (b balances) show() string {
	return fmt.Sprintf("BTC %s available, %s reserved; USDT %s available, %s reserved",
		domain.BTC.Format(b.btcAvail), domain.BTC.Format(b.btcRes), domain.USDT.Format(b.usdtAvail), domain.USDT.Format(b.usdtRes))
}

// postings renders a journal's lines: "user 2 reserved BTC -0.1000; ...".
func postings(j state.Journal) string {
	parts := make([]string, 0, len(j.Postings))
	for _, p := range j.Postings {
		owner := fmt.Sprintf("user %d %s", p.Account.User, p.Account.Bucket)
		if p.Account.User == state.SystemUser {
			owner = "system " + p.Account.Bucket.String()
		}
		amount := p.Asset.Format(p.Amount)
		if p.Amount > 0 {
			amount = "+" + amount
		}
		parts = append(parts, fmt.Sprintf("%s %s %s", owner, p.Asset, amount))
	}
	return strings.Join(parts, "; ")
}
