package telemetry

import (
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

const (
	operator domain.UserID = 99
	alice    domain.UserID = 1
	bob      domain.UserID = 2
)

// TestEngineObserverWithARealEngine drives a real engine through funding, a
// resting order, a crossing order, a retry and a rejection, and checks the
// counters say exactly that.
func TestEngineObserverWithARealEngine(t *testing.T) {
	m := New("v-test")
	e := engine.New(state.New(), engine.Config{Observer: m.Engine(), QueueSize: 16})
	m.WatchEngine(e)
	go e.Run()
	defer func() { e.Close(); <-e.Done() }()

	submit := func(user domain.UserID, key string, body command.Body) command.Result {
		t.Helper()
		res, err := e.Submit(t.Context(), user, key, body)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	submit(operator, "f1", command.Fund{To: alice, Asset: domain.USDT, Amount: 1_000_000 * domain.AtomsPerUSDT})
	submit(operator, "f2", command.Fund{To: bob, Asset: domain.BTC, Amount: 10 * domain.LotsPerBTC})
	sell := command.PlaceOrder{Side: domain.Sell, Price: 60_000, Qty: 1_000, TIF: domain.GTC}
	submit(bob, "s1", sell) // rests
	submit(bob, "s2", sell) // rests
	buy := command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 2_000, TIF: domain.IOC}
	submit(alice, "b1", buy) // fills against both: 2 fills
	submit(alice, "b1", buy) // retry: duplicate, no new fills
	// 10 BTC more, but bob has only 9.8 left after the two resting sells:
	submit(bob, "big", command.PlaceOrder{Side: domain.Sell, Price: 60_000, Qty: 100_000, TIF: domain.GTC})

	for _, tc := range []struct {
		kind, outcome string
		want          float64
	}{
		{"fund", "accepted", 2},
		{"place_order", "accepted", 3},
		{"place_order", "duplicate", 1},
		{"place_order", "insufficient_funds", 1},
		{"cancel_order", "accepted", 0},
	} {
		if got := testutil.ToFloat64(m.commands.WithLabelValues(tc.kind, tc.outcome)); got != tc.want {
			t.Errorf("commands_total{%s,%s} = %v, want %v", tc.kind, tc.outcome, got, tc.want)
		}
	}
	if got := testutil.ToFloat64(m.fills); got != 2 {
		t.Errorf("fills_total = %v, want 2 (the retry must not count again)", got)
	}

	// Without a journal there is no durable step, but every command was
	// queued, applied and completed: 7 samples each.
	for name, want := range map[string]uint64{
		"cex_engine_queue_wait_seconds": 7,
		"cex_engine_command_seconds":    7,
		"cex_engine_durable_seconds":    0,
	} {
		if got := family(t, m, name).Metric[0].Histogram.GetSampleCount(); got != want {
			t.Errorf("%s count = %d, want %d", name, got, want)
		}
	}
	if got := value(t, m, "cex_engine_applied_seq"); got != 7 {
		t.Errorf("applied_seq = %v, want 7", got)
	}
	if got := value(t, m, "cex_engine_queue_capacity"); got != 16 {
		t.Errorf("queue_capacity = %v, want 16", got)
	}
	if got := value(t, m, "cex_engine_batch_limit"); got != 64 {
		t.Errorf("batch_limit = %v, want the default 64", got)
	}
	if got := value(t, m, "cex_engine_halted"); got != 0 {
		t.Errorf("halted = %v, want 0", got)
	}

	e.Close()
	<-e.Done()
	if _, err := e.Submit(t.Context(), alice, "late", buy); err == nil {
		t.Fatal("submit after Close succeeded")
	}
	if got := testutil.ToFloat64(m.refused.WithLabelValues("closed")); got != 1 {
		t.Errorf(`refused_total{reason="closed"} = %v, want 1`, got)
	}
}

// TestDurableObservationsCountBatches: one Durable call per batch, with the
// batch size in its own histogram.
func TestDurableObservationsCountBatches(t *testing.T) {
	m := New("v-test")
	o := m.Engine()
	o.Durable(64, 0)
	o.Durable(1, 0)
	b := family(t, m, "cex_engine_batch_commands").Metric[0].Histogram
	if b.GetSampleCount() != 2 || b.GetSampleSum() != 65 {
		t.Errorf("batch histogram count %d sum %v, want 2 and 65", b.GetSampleCount(), b.GetSampleSum())
	}
}

// BenchmarkObserverOverhead: what measuring costs the engine. One caller at
// a time submits an IOC order into an empty book (no journal), with no
// observer and with the Prometheus one. The difference is the price of the
// clock readings, histogram observations and label lookups per command.
//
//	go test -run '^$' -bench ObserverOverhead -benchmem -benchtime=200000x ./internal/telemetry
func BenchmarkObserverOverhead(b *testing.B) {
	order := command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1, TIF: domain.IOC}
	for _, tc := range []struct {
		name string
		obs  func() engine.Observer
	}{
		{"none", func() engine.Observer { return nil }},
		{"prometheus", func() engine.Observer { return New("bench").Engine() }},
	} {
		b.Run(tc.name, func(b *testing.B) {
			st := state.New()
			if _, err := st.Apply(command.Command{Seq: 1, User: operator, Key: "fund", Body: command.Fund{To: alice, Asset: domain.USDT, Amount: 1_000_000 * domain.AtomsPerUSDT}}); err != nil {
				b.Fatal(err)
			}
			e := engine.New(st, engine.Config{Observer: tc.obs()})
			go e.Run()
			defer func() { e.Close(); <-e.Done() }()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := e.Submit(b.Context(), alice, strconv.Itoa(i), order); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkApplied: the cost of the observer call the owner goroutine makes
// for EVERY command, in isolation: one apply observation, one outcome
// count, one fills count.
//
//	go test -run '^$' -bench 'Applied$' -benchmem -count 5 ./internal/telemetry
func BenchmarkApplied(b *testing.B) {
	o := New("bench").Engine()
	body := command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1, TIF: domain.GTC}
	res := command.Result{Seq: 1}
	b.ReportAllocs()
	for b.Loop() {
		o.Applied(body, res, 3*time.Microsecond)
	}
}

// TestVocabularyIndices pins the hot path's array indices to their labels:
// every command kind and every outcome lands on the right series.
func TestVocabularyIndices(t *testing.T) {
	for body, want := range map[command.Body]string{
		command.PlaceOrder{}: "place_order", command.CancelOrder{}: "cancel_order", command.Fund{}: "fund",
	} {
		if got := commandKinds[kindIndex(body)]; got != want {
			t.Errorf("%T -> %q, want %q", body, got, want)
		}
	}
	for _, tc := range []struct {
		res  command.Result
		want string
	}{
		{command.Result{}, "accepted"},
		{command.Result{Duplicate: true}, "duplicate"},
		{command.Result{Duplicate: true, Rejected: command.RejectInsufficientFunds}, "duplicate"},
		{command.Result{Rejected: command.RejectInvalid}, "invalid"},
		{command.Result{Rejected: command.RejectInsufficientFunds}, "insufficient_funds"},
		{command.Result{Rejected: command.RejectUnknownOrder}, "unknown_order"},
		{command.Result{Rejected: command.RejectKeyConflict}, "key_conflict"},
		{command.Result{Rejected: command.RejectSupplyCap}, "supply_cap"},
	} {
		if got := outcomes[outcomeIndex(tc.res)]; got != tc.want {
			t.Errorf("%+v -> %q, want %q", tc.res, got, tc.want)
		}
	}
	if outcomeIndex(command.Result{Rejected: 200}) != -1 {
		t.Error("an unknown rejection reason must take the slow path (-1)")
	}
}
