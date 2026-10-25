package marketdata

import (
	"encoding/json"
	"strconv"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
)

// The wire format: one JSON text message per update, each a COMPLETE
// picture (full depth plus the recent trade tape), tagged with the Seq it
// reflects:
//
//	{"type":"snapshot","seq":"42","symbol":"BTC-USDT",
//	 "bids":[{"price":"60000","quantity":"0.3500","orders":"2"}],
//	 "asks":[...],
//	 "trades":[{"seq":"41","price":"60100","quantity":"0.1000","side":"buy"}]}
//
// Full snapshots (rather than incremental updates) make the client trivial
// and robust: any single message is enough to draw the screen, and a
// dropped connection is fixed by simply reconnecting. Incremental updates
// with gap detection are a Tier 2 refinement (plan §12).
//
// Like the REST API, every number is a STRING in exact decimal form, so
// JavaScript never rounds a price or a quantity.

type message struct {
	Type   string  `json:"type"`
	Seq    string  `json:"seq"`
	Symbol string  `json:"symbol"`
	Bids   []level `json:"bids"`
	Asks   []level `json:"asks"`
	Trades []trade `json:"trades"`
}

type level struct {
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
	Orders   string `json:"orders"`
}

type trade struct {
	Seq      string `json:"seq"`
	Price    string `json:"price"`
	Quantity string `json:"quantity"`
	Side     string `json:"side"` // the aggressor's side
}

func encode(s *engine.Snapshot) []byte {
	m := message{
		Type: "snapshot", Seq: seq(s.Seq), Symbol: "BTC-USDT",
		Bids: levels(s.Book.Bids), Asks: levels(s.Book.Asks), Trades: []trade{},
	}
	for _, t := range s.Trades {
		m.Trades = append(m.Trades, trade{seq(t.Seq), domain.FormatPrice(t.Price), domain.FormatQty(t.Qty), t.TakerSide.String()})
	}
	b, err := json.Marshal(m)
	if err != nil { // plain strings and slices cannot fail to marshal
		panic("marketdata: encoding a snapshot: " + err.Error())
	}
	return b
}

func levels(in []book.LevelSummary) []level {
	out := make([]level, 0, len(in))
	for _, l := range in {
		out = append(out, level{domain.FormatPrice(l.Price), domain.FormatQty(l.Qty), strconv.Itoa(l.Orders)})
	}
	return out
}

func seq(s domain.Seq) string { return strconv.FormatUint(uint64(s), 10) }
