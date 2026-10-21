// Package node assembles one durable exchange: it recovers the state from
// the journal on disk and starts the engine on top of it.
//
//	Open:  journal.Open ── replay every command through state.Apply
//	                    ── check every invariant
//	                    ── fingerprint the recovered state (Digest)
//	                    ── engine.New(state, journal) and start it
//	Close: stop admitting ── drain the engine ── close the journal
//
// cmd/cexd, the crash harness (tests/crash) and the node tests all start
// the exchange through Open, so the recovery path that is tested is exactly
// the one production runs.
package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/journal"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// Config describes where and how to run.
type Config struct {
	// Dir is the data directory. The journal is Dir/journal.wal. It must be
	// on persistent storage: losing it loses the exchange.
	Dir string
	// Engine sizes the queue and snapshots. Its Journal field is set by Open.
	Engine engine.Config
	// JournalHooks are test-only crash points (see journal.Hooks).
	JournalHooks journal.Hooks
}

// Recovery reports what Open found on disk.
type Recovery struct {
	Commands       int           // commands replayed from the journal
	LastSeq        domain.Seq    // the last of them
	TruncatedBytes int64         // a torn tail that was cut off (0 normally)
	Digest         [32]byte      // fingerprint of the recovered state
	Took           time.Duration // wall time of the whole recovery
}

// Node is a running exchange.
type Node struct {
	Engine   *engine.Engine
	Recovery Recovery
	journal  *journal.Journal
}

// Open recovers the exchange from cfg.Dir and starts it. If anything is
// wrong (a corrupt journal, a command the current code cannot apply, a
// broken invariant), Open fails and nothing starts. Trading on a state that
// may be missing commands would be worse than not trading.
func Open(cfg Config) (*Node, error) {
	start := time.Now() // the shell may read the clock; the state machine never does
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("node: %w", err)
	}

	st := state.New()
	j, scan, err := journal.Open(filepath.Join(cfg.Dir, journal.FileName), journal.Options{Hooks: cfg.JournalHooks}, replayInto(st))
	if err != nil {
		return nil, fmt.Errorf("node: recovering the journal: %w", err)
	}
	if err := st.CheckInvariants(); err != nil {
		_ = j.Close()
		return nil, fmt.Errorf("node: recovered state is inconsistent: %w", err)
	}

	rec := Recovery{
		Commands:       scan.Frames,
		LastSeq:        scan.LastSeq,
		TruncatedBytes: scan.TornBytes,
		Digest:         st.Digest(),
	}
	ecfg := cfg.Engine
	ecfg.Journal = j
	e := engine.New(st, ecfg) // the state now belongs to the engine
	go e.Run()
	rec.Took = time.Since(start)
	return &Node{Engine: e, Recovery: rec, journal: j}, nil
}

// replayInto applies each recovered command to st. A Go error from Apply
// (impossible ordering) or a panic (a broken money guard) means the log and
// this version of the code disagree. That is turned into an error, so
// startup stops with a message instead of a stack trace.
func replayInto(st *state.State) func(command.Command) error {
	return func(c command.Command) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic replaying seq %d: %v", c.Seq, r)
			}
		}()
		_, err = st.Apply(c)
		return err
	}
}

// Ready reports whether the node may receive trading traffic: nil if so,
// otherwise why not. It is what /readyz will answer.
func (n *Node) Ready() error {
	if err := n.Engine.Err(); err != nil {
		return fmt.Errorf("engine halted: %w", err)
	}
	return nil
}

// Close stops admitting requests, lets the engine finish everything already
// admitted (each of those is journaled and synced as it runs), then closes
// the journal file.
func (n *Node) Close() error {
	n.Engine.Close()
	<-n.Engine.Done()
	return errors.Join(n.Engine.Err(), n.journal.Close())
}
