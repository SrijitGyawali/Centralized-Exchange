package journal

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// What does durability cost? Compare encoding a frame (pure CPU) with
// appending it durably (write + fsync). Run the fsync benchmark with a
// FIXED, small count, because each iteration waits for the disk:
//
//	go test -run '^$' -bench . -benchmem -benchtime=2000x ./internal/journal
//
// Results depend almost entirely on the storage device, its cache settings
// and the OS. That is exactly why they are measured, not assumed.

var benchCmd = command.Command{
	User: 7, Key: "order-123456",
	Body: command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1_000, TIF: domain.GTC},
}

// BenchmarkEncodeFrame: codec + header + two CRCs, into a reused buffer.
func BenchmarkEncodeFrame(b *testing.B) {
	var buf []byte
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		c := benchCmd
		c.Seq = domain.Seq(i + 1)
		var err error
		if buf, err = appendFrame(buf[:0], c); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAppendSync: one durable append per iteration, the cost every
// command now pays before it is applied.
func BenchmarkAppendSync(b *testing.B) {
	j, _, err := Open(filepath.Join(b.TempDir(), FileName), Options{}, nil)
	if err != nil {
		b.Fatal(err)
	}
	defer j.Close()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		c := benchCmd
		c.Seq = domain.Seq(i + 1)
		if err := j.Append(c); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAppendBatch reports cost per command, amortizing one sync over frames.
func BenchmarkAppendBatch(b *testing.B) {
	for _, n := range []int{1, 64} {
		b.Run(fmt.Sprintf("BatchSize%d", n), func(b *testing.B) {
			j, _, err := Open(filepath.Join(b.TempDir(), FileName), Options{}, nil)
			if err != nil {
				b.Fatal(err)
			}
			defer j.Close()
			cmds := make([]command.Command, n)
			var seq domain.Seq
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for k := range cmds {
					seq++
					cmds[k] = benchCmd
					cmds[k].Seq = seq
				}
				if err := j.AppendBatch(cmds); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/command")
		})
	}
}
