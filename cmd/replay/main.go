// Command replay is the offline inspector. It reads the journal, replays it
// through the same deterministic state.Apply the server uses, and reports
// what happened.
//
// Planned usage (week 4, polished in week 8):
//
//	replay -data-dir ./data                  # replay everything, print state digest
//	replay -data-dir ./data -to-seq 1200     # stop at sequence 1200
//	replay -data-dir ./data -explain 42      # trace order 42: request, priority,
//	                                         # fills, reservations, ledger postings
//
// Because it never opens a network port or the live server's state, it is
// safe to run against a copy of production data during an incident.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "replay: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	// A dedicated FlagSet (instead of the global flag.CommandLine) keeps
	// run testable: tests can call run with any argument list.
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	dataDir := fs.String("data-dir", "./data", "directory containing the WAL")
	toSeq := fs.Uint64("to-seq", 0, "stop after this sequence (0 = replay everything)")
	explain := fs.Uint64("explain", 0, "order ID to explain (0 = none)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	_, _, _ = dataDir, toSeq, explain
	return errors.New("not implemented yet: arrives with the journal in week 4")
}
