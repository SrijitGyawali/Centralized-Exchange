package engine

import (
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// TestSnapshotCarriesRecentPublicTrades: fills appear in the snapshot's
// trade tape (newest last), duplicates add nothing, and the tape is capped.
func TestSnapshotCarriesRecentPublicTrades(t *testing.T) {
	e := New(state.New(), DefaultConfig())
	runInBackground(t, e)
	const seller domain.UserID = 2
	mustSubmit(t, e, operator, newKey(), fundAlice(usdt(10_000_000)))
	mustSubmit(t, e, operator, newKey(), command.Fund{To: seller, Asset: domain.BTC, Amount: 100 * domain.LotsPerBTC})
	mustSubmit(t, e, seller, "ask", command.PlaceOrder{Side: domain.Sell, Price: 100, Qty: 100_000, TIF: domain.GTC})

	r := mustSubmit(t, e, alice, "buy-1", buy(100, 1_000, domain.IOC))
	tape := e.Snapshot().Trades
	want := PublicTrade{Seq: r.Seq, Price: 100, Qty: 1_000, TakerSide: domain.Buy}
	if len(tape) != 1 || tape[0] != want {
		t.Fatalf("tape = %+v, want [%+v]", tape, want)
	}

	mustSubmit(t, e, alice, "buy-1", buy(100, 1_000, domain.IOC)) // a retry: no new fill
	if got := len(e.Snapshot().Trades); got != 1 {
		t.Errorf("a duplicate added trades: tape has %d", got)
	}

	for i := range RecentTrades + 10 {
		mustSubmit(t, e, alice, newKey(), buy(100, int64(1+i%3), domain.IOC))
	}
	tape = e.Snapshot().Trades
	if len(tape) != RecentTrades || tape[len(tape)-1].Seq != e.Snapshot().Seq {
		t.Errorf("tape has %d trades ending at seq %d; want %d ending at the latest seq %d",
			len(tape), tape[len(tape)-1].Seq, RecentTrades, e.Snapshot().Seq)
	}
	// An old snapshot keeps its own copy: later trades never change it.
	old := e.Snapshot()
	first := old.Trades[0]
	mustSubmit(t, e, alice, newKey(), buy(100, 7, domain.IOC))
	if old.Trades[0] != first {
		t.Error("a published snapshot's trade tape changed afterwards")
	}
}
