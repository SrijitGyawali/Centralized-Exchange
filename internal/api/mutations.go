package api

import (
	"errors"
	"io"
	"net/http"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

func (s *server) fund(w http.ResponseWriter, r *http.Request) {
	if !caller(r).Admin {
		fail(w, 403, "admin required")
		return
	}
	key, ok := mutationKey(w, r)
	if !ok {
		return
	}
	f, ok := decodeFields(w, r, "user_id", "asset", "amount")
	if !ok {
		return
	}
	u, err := parseID(f["user_id"])
	if err != nil {
		fail(w, 400, "invalid user_id")
		return
	}
	var asset domain.Asset
	switch f["asset"] {
	case "BTC":
		asset = domain.BTC
	case "USDT":
		asset = domain.USDT
	default:
		fail(w, 400, "invalid asset")
		return
	}
	n, err := domain.ParseAmount(asset, f["amount"])
	if err != nil {
		fail(w, 400, "invalid amount")
		return
	}
	s.submit(w, r, key, command.Fund{To: domain.UserID(u), Asset: asset, Amount: n})
}
func (s *server) place(w http.ResponseWriter, r *http.Request) {
	key, ok := mutationKey(w, r)
	if !ok {
		return
	}
	f, ok := decodeFields(w, r, "symbol", "type", "side", "time_in_force", "price", "quantity")
	if !ok {
		return
	}
	if f["symbol"] != "BTC-USDT" || f["type"] != "limit" {
		fail(w, 400, "unsupported symbol or type")
		return
	}
	var side domain.Side
	switch f["side"] {
	case "buy":
		side = domain.Buy
	case "sell":
		side = domain.Sell
	default:
		fail(w, 400, "invalid side")
		return
	}
	var tif domain.TimeInForce
	switch f["time_in_force"] {
	case "GTC":
		tif = domain.GTC
	case "IOC":
		tif = domain.IOC
	default:
		fail(w, 400, "invalid time_in_force")
		return
	}
	p, err := domain.ParsePrice(f["price"])
	if err != nil {
		fail(w, 400, "invalid price")
		return
	}
	q, err := domain.ParseQty(f["quantity"])
	if err != nil {
		fail(w, 400, "invalid quantity")
		return
	}
	s.submit(w, r, key, command.PlaceOrder{Side: side, Price: p, Qty: q, TIF: tif})
}
func (s *server) cancel(w http.ResponseWriter, r *http.Request) {
	key, ok := mutationKey(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, 413, "body too large")
		} else {
			fail(w, 400, "invalid body")
		}
		return
	}
	if len(body) != 0 {
		fail(w, 400, "DELETE does not accept a body")
		return
	}
	v, err := parseID(r.PathValue("id"))
	if err != nil {
		fail(w, 400, "invalid order id")
		return
	}
	s.submit(w, r, key, command.CancelOrder{Order: domain.OrderID(v)})
}
