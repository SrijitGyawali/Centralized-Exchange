package node

import (
	"strconv"
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
