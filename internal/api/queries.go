package api

import (
	"net/http"
	"strconv"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
)

func (s *server) order(w http.ResponseWriter, r *http.Request) {
	v, err := parseID(r.PathValue("id"))
	if err != nil {
		fail(w, 400, "invalid order id")
		return
	}
	u := caller(r).User
	o, err := engine.Query(r.Context(), s.e, func(st engine.StateReader) book.Order {
		o, ok := st.Order(domain.OrderID(v))
		if !ok || o.User != u {
			return book.Order{}
		}
		return o
	})
	if err != nil {
		transportError(w, err)
		return
	}
	if o.ID == 0 {
		fail(w, 404, "unknown order")
		return
	}
	writeJSON(w, 200, orderView(o))
}
func (s *server) lookup(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !validKey(key) {
		fail(w, 400, "invalid request key")
		return
	}
	u := caller(r).User
	type found struct {
		r  command.Result
		ok bool
	}
	v, err := engine.Query(r.Context(), s.e, func(st engine.StateReader) found { r, ok := st.Lookup(u, key); return found{r, ok} })
	if err != nil {
		transportError(w, err)
		return
	}
	if !v.ok {
		fail(w, 404, "unknown request")
		return
	}
	respondResult(w, v.r)
}

type balanceDTO struct {
	Asset     string `json:"asset"`
	Available string `json:"available"`
	Reserved  string `json:"reserved"`
}

func (s *server) balances(w http.ResponseWriter, r *http.Request) {
	u := caller(r).User
	v, err := engine.Query(r.Context(), s.e, func(st engine.StateReader) any {
		rows := []balanceDTO{}
		for _, a := range []domain.Asset{domain.BTC, domain.USDT} {
			av, res := st.Balance(u, a)
			format := func(n int64) string {
				if a == domain.BTC {
					return domain.FormatQty(domain.Qty(n))
				}
				return domain.FormatAtoms(domain.Atoms(n))
			}
			rows = append(rows, balanceDTO{a.String(), format(av), format(res)})
		}
		return struct {
			Seq      string       `json:"seq"`
			Balances []balanceDTO `json:"balances"`
		}{id(uint64(st.LastSeq())), rows}
	})
	if err != nil {
		transportError(w, err)
		return
	}
	writeJSON(w, 200, v)
}

type levelDTO struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
	Orders   string `json:"orders"`
}

func (s *server) book(w http.ResponseWriter, r *http.Request) {
	if err := s.e.Err(); err != nil {
		transportError(w, err)
		return
	}
	if symbols, present := r.URL.Query()["symbol"]; present && (len(symbols) != 1 || symbols[0] != "BTC-USDT") {
		fail(w, 400, "unsupported symbol")
		return
	}
	snap := s.e.Snapshot()
	levels := func(in []book.LevelSummary) []levelDTO {
		out := []levelDTO{}
		for _, v := range in {
			out = append(out, levelDTO{domain.FormatPrice(v.Price), domain.FormatQty(v.Qty), strconv.Itoa(v.Orders)})
		}
		return out
	}
	writeJSON(w, 200, struct {
		Seq    string     `json:"seq"`
		Symbol string     `json:"symbol"`
		Bids   []levelDTO `json:"bids"`
		Asks   []levelDTO `json:"asks"`
	}{id(uint64(snap.Seq)), "BTC-USDT", levels(snap.Book.Bids), levels(snap.Book.Asks)})
}
