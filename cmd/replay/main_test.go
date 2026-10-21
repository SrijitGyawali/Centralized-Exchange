package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/journal"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/node"
)

// writeJournal runs a real node, sends a few commands, shuts it down, and
// returns the data directory and the digest a restart would recover.
func writeJournal(t *testing.T) (dir string, digest [32]byte) {
	t.Helper()
	dir = t.TempDir()
	n, err := node.Open(node.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range []struct {
		user domain.UserID
		body command.Body
	}{
		{99, command.Fund{To: 1, Asset: domain.USDT, Amount: 10_000 * domain.AtomsPerUSDT}},
		{99, command.Fund{To: 2, Asset: domain.BTC, Amount: domain.LotsPerBTC}},
		{2, command.PlaceOrder{Side: domain.Sell, Price: 60_000, Qty: 2_000, TIF: domain.GTC}},
		{1, command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1_000, TIF: domain.GTC}},
	} {
		if _, err := n.Engine.Submit(t.Context(), s.user, fmt.Sprint("k", i), s.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := node.Open(node.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	return dir, again.Recovery.Digest
}

func TestReplayReportsTheRecoveredState(t *testing.T) {
	dir, digest := writeJournal(t)
	var out bytes.Buffer
	if err := run([]string{"-data-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"frames     4 valid, last seq 4",
		"invariants ok",
		fmt.Sprintf("digest     %x", digest), // the same fingerprint the server recovers
		"user 1    BTC   available 0.1000",
		"ASK    60000    0.1000   1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestReplayToSeq(t *testing.T) {
	dir, _ := writeJournal(t)
	var out bytes.Buffer
	if err := run([]string{"-data-dir", dir, "-to-seq", "3"}, &out); err != nil {
		t.Fatal(err)
	}
	// After seq 3, Bob's ask rests untouched: Alice's bid (seq 4) never happened.
	for _, want := range []string{"stopped    after seq 3", "ASK    60000    0.2000   1"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// TestReplayNeverRepairs: unlike the server, the inspector reports a torn
// tail but leaves the file exactly as it found it.
func TestReplayNeverRepairs(t *testing.T) {
	dir, _ := writeJournal(t)
	path := filepath.Join(dir, journal.FileName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("CX\x01")); err != nil { // the start of a frame, cut short
		t.Fatal(err)
	}
	f.Close()
	before, _ := os.ReadFile(path)

	var out bytes.Buffer
	if err := run([]string{"-data-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "torn tail  3 bytes") {
		t.Errorf("torn tail not reported:\n%s", out.String())
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, before) {
		t.Error("replay modified the journal")
	}
}

func TestReplayErrors(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"-data-dir", t.TempDir()}, &out); err == nil {
		t.Error("a directory without a journal: no error")
	}
	if err := run([]string{"-explain", "1"}, &out); err == nil || !strings.Contains(err.Error(), "week 8") {
		t.Errorf("-explain: error = %v, want 'arrives in week 8'", err)
	}
}
