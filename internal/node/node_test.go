package node

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/journal"
)

const (
	operator domain.UserID = 99
	alice    domain.UserID = 1
)

var (
	fund = command.Fund{To: alice, Asset: domain.USDT, Amount: 10_000 * domain.AtomsPerUSDT}
	bid  = command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1_000, TIF: domain.GTC}
)

func mustOpen(t *testing.T, dir string) *Node {
	t.Helper()
	n, err := Open(Config{Dir: dir})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return n
}

func submit(t *testing.T, n *Node, user domain.UserID, key string, body command.Body) command.Result {
	t.Helper()
	r, err := n.Engine.Submit(t.Context(), user, key, body)
	if err != nil {
		t.Fatalf("Submit(%q): %v", key, err)
	}
	return r
}

func digest(t *testing.T, n *Node) [32]byte {
	t.Helper()
	d, err := engine.Query(t.Context(), n.Engine, func(s engine.StateReader) [32]byte { return s.Digest() })
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFreshNodeStartsEmptyAndReady(t *testing.T) {
	n := mustOpen(t, t.TempDir())
	defer n.Close()
	if n.Recovery.Commands != 0 || n.Recovery.TruncatedBytes != 0 {
		t.Errorf("fresh recovery = %+v", n.Recovery)
	}
	if err := n.Ready(); err != nil {
		t.Errorf("Ready() = %v", err)
	}
}

// TestRestartRecoversEverything: state, order IDs and — crucially — the
// dedupe table survive a restart. A client that retries after the server
// restarted still gets its original result instead of a second trade.
func TestRestartRecoversEverything(t *testing.T) {
	dir := t.TempDir()
	n := mustOpen(t, dir)
	submit(t, n, operator, "fund", fund)
	placed := submit(t, n, alice, "bid", bid)
	before := digest(t, n)
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}

	n2 := mustOpen(t, dir)
	defer n2.Close()
	if n2.Recovery.Commands != 2 || n2.Recovery.LastSeq != 2 || n2.Recovery.Digest != before {
		t.Fatalf("recovery = %+v; want 2 commands and the pre-restart digest", n2.Recovery)
	}
	retry := submit(t, n2, alice, "bid", bid)
	if !retry.Duplicate || retry.Order != placed.Order || retry.Seq != placed.Seq {
		t.Errorf("retry after restart = %+v, want a duplicate of %+v", retry, placed)
	}
	small := bid
	small.Qty = 500 // 3,000 USDT: fits in the 4,000 Alice still has available
	if next := submit(t, n2, alice, "bid-2", small); next.Order != 2 || next.Seq != 4 {
		t.Errorf("next order after restart = %+v, want order 2 at seq 4 (IDs and Seqs continue)", next)
	}
}

func TestCorruptJournalRefusesToStart(t *testing.T) {
	dir := t.TempDir()
	n := mustOpen(t, dir)
	submit(t, n, operator, "fund", fund)
	submit(t, n, alice, "bid", bid)
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, journal.FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/3] ^= 0x10 // one flipped bit somewhere inside
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(Config{Dir: dir}); !errors.Is(err, journal.ErrCorrupt) {
		t.Fatalf("Open on a corrupt journal: error = %v, want ErrCorrupt", err)
	}
}
