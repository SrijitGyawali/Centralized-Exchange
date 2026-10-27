package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"
)

// Workload is one versioned experiment definition (loadtest/workloads/*.json).
// Everything that shapes the offered load lives in this file, so a result
// can always be traced back to exactly what was asked of the server.
type Workload struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Seed makes the offered sequence reproducible: the same seed produces
	// the same users, sides, prices, quantities and order kinds, in the same
	// order, on every run.
	Seed uint64 `json:"seed"`
	// Users is how many trading accounts share the load.
	Users int `json:"users"`
	// Mix weights the three operations (any scale; only ratios matter).
	Mix Mix `json:"mix"`
	// MidPrice and SpreadTicks place prices: resting orders sit up to
	// SpreadTicks away from MidPrice on their own side; crossing orders reach
	// SpreadTicks into the other side.
	MidPrice    int64 `json:"mid_price"`
	SpreadTicks int64 `json:"spread_ticks"`
	// MaxQtyLots bounds a resting order's quantity (crossing orders take up
	// to twice that, so they eat into the book rather than only add to it).
	MaxQtyLots int64 `json:"max_qty_lots"`
	// FundUSDT and FundBTC are given to every user before the run, in whole
	// units. Generous on purpose: the experiment must measure trading, not
	// "insufficient funds" rejections.
	FundUSDT int64 `json:"fund_usdt"`
	FundBTC  int64 `json:"fund_btc"`
	// Warmup runs first at Stages[0].Rate and is reported separately: caches,
	// connection pools and the Go runtime settle before measuring.
	Warmup Duration `json:"warmup"`
	// Stages are run in order. Each offers a constant arrival rate.
	Stages []Stage `json:"stages"`
	// MaxInFlight bounds concurrent requests. When it is reached, an
	// arrival is DROPPED and counted, never delayed (open model).
	MaxInFlight int `json:"max_in_flight"`
	// Timeout is the client's per-request deadline.
	Timeout Duration `json:"timeout"`
}

// Mix holds the operation weights.
type Mix struct {
	Rest   int `json:"rest"`   // GTC limit order away from the market: adds liquidity
	Cross  int `json:"cross"`  // IOC limit order through the market: trades
	Cancel int `json:"cancel"` // cancel one of the user's own resting orders
}

// Stage is one constant-rate interval.
type Stage struct {
	Rate     float64  `json:"rate"` // arrivals per second
	Duration Duration `json:"duration"`
}

// Duration is a time.Duration written as "30s" in JSON.
type Duration struct{ time.Duration }

// UnmarshalJSON implements json.Unmarshaler, so a workload file can say
// "10m" instead of 600000000000.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalJSON writes the same form back, for results.json.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// LoadWorkload reads and validates a workload file. Unknown fields are an
// error: a typo such as "max_in_fligth" must not silently use a default.
func LoadWorkload(path string) (Workload, error) {
	f, err := os.Open(path)
	if err != nil {
		return Workload{}, err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	var w Workload
	if err := dec.Decode(&w); err != nil {
		return Workload{}, fmt.Errorf("%s: %w", path, err)
	}
	return w, w.validate()
}

func (w Workload) validate() error {
	var errs []string
	check := func(ok bool, msg string) {
		if !ok {
			errs = append(errs, msg)
		}
	}
	check(w.Name != "", "name is required")
	check(w.Users >= 1 && w.Users <= 1000, "users must be 1..1000")
	check(w.Mix.Rest >= 0 && w.Mix.Cross >= 0 && w.Mix.Cancel >= 0 && w.Mix.Rest+w.Mix.Cross+w.Mix.Cancel > 0, "mix weights must be non-negative with a positive sum")
	check(w.SpreadTicks >= 1 && w.MidPrice-w.SpreadTicks-1 >= 1 && w.MidPrice+w.SpreadTicks+1 <= 1_000_000, "mid_price and spread_ticks must keep every price in 1..1000000")
	check(w.MaxQtyLots >= 1 && 2*w.MaxQtyLots <= 100_000, "max_qty_lots must be 1..50000")
	check(w.FundUSDT >= 1 && w.FundBTC >= 1, "fund_usdt and fund_btc must be positive")
	check(len(w.Stages) >= 1, "at least one stage is required")
	for i, s := range w.Stages {
		check(s.Rate > 0 && s.Rate <= 100_000 && s.Duration.Duration > 0, fmt.Sprintf("stage %d needs a rate in (0, 100000] and a positive duration", i+1))
	}
	check(w.Warmup.Duration >= 0, "warmup must not be negative")
	check(w.MaxInFlight >= 1, "max_in_flight must be at least 1")
	check(w.Timeout.Duration > 0, "timeout must be positive")
	if len(errs) > 0 {
		return errors.New("invalid workload: " + strings.Join(errs, "; "))
	}
	return nil
}

// OpKind is what one arrival does.
type OpKind uint8

const (
	OpRest OpKind = iota
	OpCross
	OpCancel
)

func (k OpKind) String() string { return [...]string{"rest", "cross", "cancel"}[k] }

// Op is one generated arrival. For OpCancel the target order is chosen when
// the op is sent, from what that user actually has resting.
type Op struct {
	Kind  OpKind
	User  int    // 0-based index into the users
	Side  string // "buy" or "sell"
	Price int64  // ticks
	Qty   int64  // lots
}

// Generator produces the deterministic op sequence. It is used by the
// scheduler goroutine only, so it needs no lock.
type Generator struct {
	w   Workload
	rng *rand.Rand
}

// NewGenerator seeds a PCG generator from the workload. math/rand/v2's PCG
// is fully specified, so the sequence is the same on every platform and Go
// version, unlike a generator seeded from the clock.
func NewGenerator(w Workload) *Generator {
	return &Generator{w: w, rng: rand.New(rand.NewPCG(w.Seed, w.Seed^0x9e3779b97f4a7c15))}
}

// Next returns the next op.
func (g *Generator) Next() Op {
	w := g.w
	op := Op{User: g.rng.IntN(w.Users)}
	buy := g.rng.IntN(2) == 0
	op.Side = "sell"
	if buy {
		op.Side = "buy"
	}
	switch n := g.rng.IntN(w.Mix.Rest + w.Mix.Cross + w.Mix.Cancel); {
	case n < w.Mix.Rest:
		op.Kind = OpRest
		away := 1 + g.rng.Int64N(w.SpreadTicks) // never at or through the mid: it rests
		op.Price = w.MidPrice + away
		if buy {
			op.Price = w.MidPrice - away
		}
		op.Qty = 1 + g.rng.Int64N(w.MaxQtyLots)
	case n < w.Mix.Rest+w.Mix.Cross:
		op.Kind = OpCross
		op.Price = w.MidPrice - w.SpreadTicks - 1 // a sell reaching deep into the bids
		if buy {
			op.Price = w.MidPrice + w.SpreadTicks + 1
		}
		op.Qty = 1 + g.rng.Int64N(2*w.MaxQtyLots)
	default:
		op.Kind = OpCancel
	}
	return op
}
