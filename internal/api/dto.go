package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
)

func id(v uint64) string { return strconv.FormatUint(v, 10) }
func parseID(s string) (uint64, error) {
	v, e := strconv.ParseUint(s, 10, 64)
	if e != nil || v == 0 || id(v) != s {
		return 0, errors.New("invalid identifier")
	}
	return v, nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

type fillDTO struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
}
type resultDTO struct {
	Seq       string    `json:"seq"`
	Accepted  bool      `json:"accepted"`
	Rejected  string    `json:"rejected"`
	Detail    string    `json:"detail"`
	Duplicate bool      `json:"duplicate"`
	OrderID   string    `json:"order_id"`
	Outcome   string    `json:"outcome"`
	Fills     []fillDTO `json:"fills"`
	Remaining string    `json:"remaining"`
	Canceled  string    `json:"canceled"`
}

func resultView(r command.Result) resultDTO {
	v := resultDTO{Seq: id(uint64(r.Seq)), Accepted: r.Accepted(), Rejected: r.Rejected.String(), Detail: r.Detail, Duplicate: r.Duplicate, OrderID: id(uint64(r.Order)), Remaining: domain.FormatQty(r.Remaining), Canceled: domain.FormatQty(r.Canceled), Fills: []fillDTO{}}
	if r.Outcome != 0 {
		v.Outcome = r.Outcome.String()
	}
	for _, f := range r.Fills {
		v.Fills = append(v.Fills, fillDTO{domain.FormatPrice(f.Price), domain.FormatQty(f.Qty)})
	}
	return v
}
func respondResult(w http.ResponseWriter, r command.Result) {
	status := 200
	switch r.Rejected {
	case command.RejectInvalid, command.RejectInsufficientFunds, command.RejectSupplyCap:
		status = 422
	case command.RejectUnknownOrder:
		status = 404
	case command.RejectKeyConflict:
		status = 409
	}
	writeJSON(w, status, resultView(r))
}
func transportError(w http.ResponseWriter, err error) {
	status := 503
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		status = 504
	}
	if errors.Is(err, engine.ErrOutcomeUnknown) {
		writeJSON(w, status, map[string]any{"error": "outcome_unknown", "outcome_unknown": true, "detail": "Retry the same Idempotency-Key or query /v1/requests/{key}."})
		return
	}
	fail(w, status, "service_unavailable")
}
func (s *server) submit(w http.ResponseWriter, r *http.Request, key string, b command.Body) {
	res, err := s.e.Submit(r.Context(), caller(r).User, key, b)
	if err != nil {
		transportError(w, err)
		return
	}
	respondResult(w, res)
}

type orderDTO struct {
	OrderID  string `json:"order_id"`
	Symbol   string `json:"symbol"`
	Side     string `json:"side"`
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
	Seq      string `json:"seq"`
}

func orderView(o book.Order) orderDTO {
	return orderDTO{id(uint64(o.ID)), "BTC-USDT", o.Side.String(), domain.FormatPrice(o.Price), domain.FormatQty(o.Qty), id(uint64(o.Seq))}
}
