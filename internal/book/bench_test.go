package book

import (
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// Microbenchmarks for the three basic book operations, measured separately
// so each can be tracked on its own. Run them with:
//
//	go test -run '^$' -bench . -benchmem ./internal/book
//
// -run '^$' skips the ordinary tests; -benchmem reports bytes and
// allocations per operation.
//
// The numbers describe one machine, one Go version and these synthetic books
// (100 price levels, 4,096 orders). They are a baseline for later
// comparisons, not a performance claim: in the real system the WAL's fsync
// will cost far more than matching.

// chunk is how many orders each benchmark puts in a book before building a
// fresh one. It keeps memory bounded however large b.N grows.
const chunk = 4096

// The classic `for i := 0; i < b.N; i++` loop is used instead of b.Loop
// because each benchmark pauses the timer (StopTimer/StartTimer) to rebuild
// its book every chunk, so setup is never measured.

// BenchmarkPlaceResting: a GTC buy that does not cross joins the back of one
// of 100 bid levels (a new level the first time each price is used).
func BenchmarkPlaceResting(b *testing.B) {
	b.ReportAllocs()
	var bk *Book
	var seq uint64
	for i := 0; i < b.N; i++ {
		if i%chunk == 0 {
			b.StopTimer()
			bk, seq = New(), 0
			b.StartTimer()
		}
		seq++
		o := Order{ID: domain.OrderID(seq), User: 1, Side: domain.Buy, Price: domain.Price(1_000 - seq%100), Qty: 10, Seq: domain.Seq(seq)}
		if _, err := bk.Place(o, domain.GTC); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkPlaceCrossing: an IOC buy that exactly fills the oldest order at
// the best ask, so each operation produces one trade and removes one maker
// (and, every ~41 orders, an emptied price level).
func BenchmarkPlaceCrossing(b *testing.B) {
	b.ReportAllocs()
	var bk *Book
	var seq uint64
	for i := 0; i < b.N; i++ {
		if i%chunk == 0 {
			b.StopTimer()
			bk, seq = prefilled(b, domain.Sell), chunk
			b.StartTimer()
		}
		seq++
		o := Order{ID: domain.OrderID(seq), User: 2, Side: domain.Buy, Price: 2_000, Qty: 10, Seq: domain.Seq(seq)}
		res, err := bk.Place(o, domain.IOC)
		if err != nil || len(res.Fills) != 1 {
			b.Fatalf("Place = %+v, %v; want exactly one fill", res, err)
		}
	}
}

// BenchmarkCancel: cancel resting orders in a scrambled order, so most are
// removed from the middle of a queue rather than its head.
func BenchmarkCancel(b *testing.B) {
	b.ReportAllocs()
	var bk *Book
	for i := 0; i < b.N; i++ {
		if i%chunk == 0 {
			b.StopTimer()
			bk = prefilled(b, domain.Buy)
			b.StartTimer()
		}
		// j*1597 mod 4096 visits every j exactly once (1597 is odd and 4096 a
		// power of two), so this is a cheap permutation of the order IDs.
		id := domain.OrderID((i%chunk)*1597%chunk + 1)
		if _, err := bk.Cancel(id); err != nil {
			b.Fatal(err)
		}
	}
}

// prefilled returns a book holding `chunk` resting orders of 10 lots each on
// one side, spread round-robin over 100 price levels, with IDs and Seqs
// 1..chunk. All belong to user 1.
func prefilled(b *testing.B, side domain.Side) *Book {
	b.Helper()
	bk := New()
	for id := uint64(1); id <= chunk; id++ {
		price := domain.Price(1_000 + id%100) // asks: 1000..1099
		if side == domain.Buy {
			price = domain.Price(1_000 - id%100) // bids: 901..1000
		}
		o := Order{ID: domain.OrderID(id), User: 1, Side: side, Price: price, Qty: 10, Seq: domain.Seq(id)}
		if _, err := bk.Place(o, domain.GTC); err != nil {
			b.Fatal(err)
		}
	}
	return bk
}
