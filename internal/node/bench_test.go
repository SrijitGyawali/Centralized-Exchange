package node

import (
	"fmt"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// BenchmarkSubmitDurable measures one command end to end through a real
// node: queue, Seq, journal append + fsync, Apply, snapshot, reply. Compare
// it with internal/engine's BenchmarkSubmit (the same path minus the
// journal) to see what durability costs.
//
//	go test -run '^$' -bench . -benchmem -benchtime=2000x ./internal/node
func BenchmarkSubmitDurable(b *testing.B) {
	n, err := Open(Config{Dir: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	defer n.Close()
	if _, err := n.Engine.Submit(b.Context(), operator, "fund", fund); err != nil {
		b.Fatal(err)
	}
	ioc := command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1, TIF: domain.IOC} // reserve, find nothing, release
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if _, err := n.Engine.Submit(b.Context(), alice, strconv.Itoa(i), ioc); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSubmitDurableConcurrent exposes group commit under concurrent load.
func BenchmarkSubmitDurableConcurrent(b *testing.B) {
	for _, size := range []int{1, 64} {
		b.Run(fmt.Sprintf("BatchSize%d", size), func(b *testing.B) {
			n, err := Open(Config{Dir: b.TempDir(), Engine: engine.Config{BatchSize: size, QueueSize: 4096}})
			if err != nil {
				b.Fatal(err)
			}
			defer n.Close()
			if _, err := n.Engine.Submit(b.Context(), operator, "fund", fund); err != nil {
				b.Fatal(err)
			}
			var keys atomic.Uint64
			ioc := command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1, TIF: domain.IOC}
			b.ReportAllocs()
			// Keep 64 callers active regardless of the host CPU count.
			b.SetParallelism((64 + runtime.GOMAXPROCS(0) - 1) / runtime.GOMAXPROCS(0))
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := n.Engine.Submit(b.Context(), alice, strconv.FormatUint(keys.Add(1), 10), ioc); err != nil {
						b.Error(err)
						return
					}
				}
			})
		})
	}
}
