// Command replay is the offline inspector. It reads a journal WITHOUT
// changing it (no repair, no truncation), replays it through the same
// state.Apply the server uses, and reports what it finds.
//
//	replay -data-dir ./data              # replay everything
//	replay -data-dir ./data -to-seq 6    # the state as it was right after seq 6
//	replay -data-dir ./data -explain 3   # the life of order 3, request to ledger
//	replay -data-dir ./data -explain-seq 5  # one command: request, result, postings
//
// It prints the journal's health (frames, any torn tail), whether every
// invariant holds, the state digest (compare it with the "digest" the
// server logs at startup), balances and the top of the book.
//
// It never opens a network port and never touches a running server, so it
// is safe to run against a COPY of production data during an incident.
//
// -explain and -explain-seq recompute everything from the journal
// with the same state.Apply the server ran (explain.go): nothing comes from
// logs or SQL, so the explanation is exactly what the exchange decided.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/journal"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "replay: %v\n", err)
		os.Exit(1)
	}
}

// errStop ends the scan early at -to-seq. It is a sentinel, not a failure.
var errStop = errors.New("reached -to-seq")

func run(args []string, out io.Writer) error {
	// A dedicated FlagSet (instead of the global flag.CommandLine) keeps
	// run testable: tests can call run with any argument list.
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "./data", "directory containing "+journal.FileName)
	toSeq := fs.Uint64("to-seq", 0, "stop after this sequence (0 = replay everything)")
	explain := fs.Uint64("explain", 0, "order ID to trace from request to ledger postings")
	explainSeq := fs.Uint64("explain-seq", 0, "command Seq to show: request, result, ledger postings")
	if err := fs.Parse(args); err != nil {
		return err
	}

	path := filepath.Join(*dataDir, journal.FileName)
	f, err := os.Open(path) // read-only: replay must never modify the evidence
	if err != nil {
		return err
	}
	defer f.Close()

	st := state.New()
	t := newTracer(*explain, *explainSeq)
	res, err := journal.Scan(f, func(c command.Command) error {
		if *toSeq != 0 && uint64(c.Seq) > *toSeq {
			return errStop
		}
		return t.apply(st, c)
	})
	stopped := errors.Is(err, errStop)
	if err != nil && !stopped {
		return err
	}

	if *explain != 0 || *explainSeq != 0 {
		if *explain != 0 {
			if err := t.explainOrder(out, st); err != nil {
				return err
			}
		}
		if *explainSeq != 0 {
			if err := t.explainSeq(out); err != nil {
				return err
			}
		}
		invariants := "ok"
		if err := st.CheckInvariants(); err != nil {
			invariants = "BROKEN: " + err.Error()
		}
		fmt.Fprintf(out, "state      seq %d, invariants %s\ndigest     %x\n", st.LastSeq(), invariants, st.Digest())
		return nil
	}

	fmt.Fprintf(out, "journal    %s\n", path)
	if stopped {
		fmt.Fprintf(out, "stopped    after seq %d (-to-seq)\n", st.LastSeq())
	} else {
		fmt.Fprintf(out, "frames     %d valid, last seq %d, %d bytes\n", res.Frames, res.LastSeq, res.ValidBytes)
		if res.TornBytes > 0 {
			fmt.Fprintf(out, "torn tail  %d bytes: a crash cut the last write short; the server drops them on its next start\n", res.TornBytes)
		}
	}
	invariants := "ok"
	if err := st.CheckInvariants(); err != nil {
		invariants = "BROKEN: " + err.Error()
	}
	fmt.Fprintf(out, "state      seq %d, invariants %s\n", st.LastSeq(), invariants)
	fmt.Fprintf(out, "digest     %x\n", st.Digest())

	fmt.Fprintln(out, "\nbalances")
	for _, b := range st.Balances() {
		fmt.Fprintf(out, "  user %-4d %-4v  available %s  reserved %s\n",
			b.User, b.Asset, b.Asset.Format(b.Available), b.Asset.Format(b.Reserved))
	}
	fmt.Fprintf(out, "\nbook (top 10 levels per side)\n%v", st.Depth(10))
	return nil
}
