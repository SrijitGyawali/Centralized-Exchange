package engine

import (
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// How much does going through the engine cost, compared with calling
// state.Apply directly? Run with a FIXED iteration count:
//
//	go test -run '^$' -bench . -benchmem -benchtime=200000x ./internal/engine
//
// Every command is remembered in the dedupe table (ADR-0004), so memory
// grows with b.N. A fixed count keeps runs comparable and bounded.
//
// The command is an IOC buy into an empty book: it reserves 6 USDT, finds
// nothing to trade and releases it again (two journals, no book change), so
// each iteration does the same work.

var benchOrder = command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1, TIF: domain.IOC}

func fundedState(b *testing.B) *state.State {
	b.Helper()
	st := state.New()
	_, err := st.Apply(command.Command{Seq: 1, User: operator, Key: "fund", Body: fundAlice(usdt(1_000_000))})
	if err != nil {
		b.Fatal(err)
	}
	return st
}

// BenchmarkApplyDirect: the state machine alone, the floor.
func BenchmarkApplyDirect(b *testing.B) {
	st := fundedState(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cmd := command.Command{Seq: domain.Seq(i + 2), User: alice, Key: strconv.Itoa(i), Body: benchOrder}
		if _, err := st.Apply(cmd); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSubmit: one caller at a time through the engine: enqueue, the
// owner goroutine wakes, Apply, snapshot publish, reply, the caller wakes.
func BenchmarkSubmit(b *testing.B) {
	e := New(fundedState(b), DefaultConfig())
	go e.Run()
	defer func() { e.Close(); <-e.Done() }()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := e.Submit(b.Context(), alice, strconv.Itoa(i), benchOrder); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSubmitParallel: many callers at once (GOMAXPROCS goroutines).
// The owner still applies one command at a time, so this measures how the
// single writer behaves under contention, not parallel speed-up.
func BenchmarkSubmitParallel(b *testing.B) {
	e := New(fundedState(b), Config{QueueSize: 4096})
	go e.Run()
	defer func() { e.Close(); <-e.Done() }()
	var n atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := e.Submit(b.Context(), alice, strconv.FormatUint(n.Add(1), 10), benchOrder); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
