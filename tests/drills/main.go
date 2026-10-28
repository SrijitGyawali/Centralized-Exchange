// Command drills runs the week-8 operational drills against the real cexd
// binary and writes an evidence report:
//
//  1. crash: clients trade while the server is KILLED mid-traffic, again and
//     again. After each restart: every answered request must still have its
//     answer; every request whose answer was lost is retried with the same
//     key and must take effect exactly once; refused requests must never
//     have run; the journal is audited offline (money checks).
//
//  2. restore: a journal copied while the server is live (a hot copy) and
//     one copied after it died (a cold copy) are restored on a fresh
//     directory. The copy must be an exact prefix of the original, recover
//     the same state up to its last command, and lose only what came after.
//
//  3. recovery: how long replaying 100,000 and 1,000,000 commands takes,
//     and how much memory the recovered state uses.
//
//     go run ./tests/drills -out evidence/crash/2026-10-28
//     go run ./tests/drills -out tmp/drills -crash-rounds 1 -sizes ""   # quick
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func main() {
	out := flag.String("out", "", "directory for report.md, results.json and the server logs (required)")
	rounds := flag.Int("crash-rounds", 5, "kill-and-restart rounds in the crash drill")
	clients := flag.Int("clients", 32, "concurrent trading clients")
	sizes := flag.String("sizes", "100000,1000000", "journal sizes (commands) for the recovery drill; empty = skip")
	seed := flag.Uint64("seed", 1, "seed for traffic and kill timing")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "drills: -out is required")
		os.Exit(2)
	}
	if err := run(*out, *rounds, *clients, *sizes, *seed); err != nil {
		fmt.Fprintln(os.Stderr, "drills:", err)
		os.Exit(1)
	}
}

// settings records how the drill was run.
type settings struct {
	CrashRounds int    `json:"crash_rounds"`
	Clients     int    `json:"clients"`
	Users       int    `json:"users"`
	Seed        uint64 `json:"seed"`
	Sizes       string `json:"recovery_sizes"`
}

// report is everything written to results.json.
type report struct {
	StartedAt string        `json:"started_at"`
	Commit    string        `json:"git_commit"`
	OS        string        `json:"os"`
	CPUs      int           `json:"logical_cpus"`
	Go        string        `json:"go"`
	Crash     []crashRound  `json:"crash_rounds"`
	Restore   []restoreRun  `json:"restores"`
	Recovery  []recoveryRun `json:"recovery"`
	Failures  []string      `json:"failures"` // every check that did not hold
	Settings  settings      `json:"settings"`
}

func (r *report) fail(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	r.Failures = append(r.Failures, msg)
	fmt.Fprintln(os.Stderr, "FAILED:", msg)
}

func run(out string, rounds, clients int, sizes string, seed uint64) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "cex-drills-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	bin, err := buildServer(work)
	if err != nil {
		return err
	}
	rep := &report{
		StartedAt: time.Now().UTC().Format(time.RFC3339), Commit: gitCommit(),
		OS: runtime.GOOS + "/" + runtime.GOARCH, CPUs: runtime.NumCPU(), Go: runtime.Version(),
		Settings: settings{CrashRounds: rounds, Clients: clients, Users: users, Seed: seed, Sizes: sizes},
	}
	rng := rand.New(rand.NewPCG(seed, 99))

	fmt.Println("== crash drill")
	if err := crashDrill(rep, bin, work, out, rounds, clients, seed, rng); err != nil {
		return err
	}
	fmt.Println("== restore drill")
	if err := restoreDrill(rep, bin, work, out, clients, seed); err != nil {
		return err
	}
	if sizes != "" {
		fmt.Println("== recovery drill")
		for _, s := range strings.Split(sizes, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || n <= 0 {
				return fmt.Errorf("bad -sizes entry %q", s)
			}
			r, err := recoveryDrill(filepath.Join(work, "recovery-"+s), n, seed)
			if err != nil {
				return err
			}
			rep.Recovery = append(rep.Recovery, r)
			fmt.Printf("   %d commands: journal %.1f MiB, recovery %s (median of %d), heap %.0f MiB\n",
				r.Commands, float64(r.JournalBytes)/(1<<20), r.RecoveryMedian, len(r.Recoveries), r.HeapMiB)
		}
	}

	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "results.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "report.md"), []byte(markdown(rep)), 0o644); err != nil {
		return err
	}
	if len(rep.Failures) > 0 {
		return fmt.Errorf("%d check(s) failed; see %s", len(rep.Failures), filepath.Join(out, "report.md"))
	}
	fmt.Println("all drills passed; report in", filepath.Join(out, "report.md"))
	return nil
}

// crashRound is one kill-and-restart cycle.
type crashRound struct {
	Round            int       `json:"round"`
	TrafficBeforeMs  int64     `json:"traffic_before_kill_ms"`
	Requests         int       `json:"requests"`
	Answered         int       `json:"answered"`
	Refused          int       `json:"refused"`
	Unknown          int       `json:"unknown_outcome"`
	Offline          audit     `json:"offline_audit_after_kill"`
	Restart          recovered `json:"restart_recovery"`
	ReadyMs          int64     `json:"restart_ready_ms"`
	AnsweredKept     int       `json:"answered_found_unchanged"`
	RefusedNeverRan  int       `json:"refused_confirmed_never_ran"`
	UnknownWasDone   int       `json:"unknown_already_done_before_crash"`
	UnknownRanNow    int       `json:"unknown_executed_by_retry"`
	UnknownExactOnce int       `json:"unknown_exactly_once"`
}

func crashDrill(rep *report, bin, work, out string, rounds, clients int, seed uint64, rng *rand.Rand) error {
	s := &server{bin: bin, dataDir: filepath.Join(work, "crash-data"), logPath: filepath.Join(out, "crash-server.log")}
	_ = os.Remove(s.logPath)
	if _, _, err := s.start(); err != nil {
		return err
	}
	defer s.kill()
	ctx := context.Background()
	if _, err := fund(ctx, s.base, "crash"); err != nil {
		return err
	}
	for round := 1; round <= rounds; round++ {
		cr := crashRound{Round: round}
		tctx, cancel := context.WithCancel(ctx)
		done := make(chan []request, 1)
		go func() { done <- traffic(tctx, s.base, fmt.Sprintf("r%d", round), clients, seed+uint64(round)) }()
		wait := time.Duration(1500+rng.IntN(2000)) * time.Millisecond // kill at a random moment
		time.Sleep(wait)
		s.kill() // mid-traffic: requests are in flight, some batches mid-fsync
		cancel()
		reqs := <-done
		cr.TrafficBeforeMs = wait.Milliseconds()

		offline, err := auditJournal(filepath.Join(s.dataDir, "journal.wal"), 0)
		if err != nil {
			return err
		}
		cr.Offline = offline
		ready, rec, err := s.start()
		if err != nil {
			return err
		}
		cr.ReadyMs, cr.Restart = ready.Milliseconds(), rec
		if rec.Digest != offline.Digest || rec.LastSeq != offline.LastSeq || rec.TruncatedBytes != offline.TornBytes {
			rep.fail("round %d: restart recovered seq %d digest %.12s torn %d, offline replay says seq %d digest %.12s torn %d",
				round, rec.LastSeq, rec.Digest, rec.TruncatedBytes, offline.LastSeq, offline.Digest, offline.TornBytes)
		}
		if offline.Invariants != "ok" || !offline.Conserved {
			rep.fail("round %d: money check: invariants %s, conserved %v", round, offline.Invariants, offline.Conserved)
		}

		var answered, refused, unknown []request
		for _, r := range reqs {
			cr.Requests++
			switch {
			case r.Answered:
				answered = append(answered, r)
			case r.Refused:
				refused = append(refused, r)
			default:
				unknown = append(unknown, r)
			}
		}
		cr.Answered, cr.Refused, cr.Unknown = len(answered), len(refused), len(unknown)

		// 1. Every answer survived the crash, unchanged.
		lost := 0
		for i, l := range parallel(answered, 16, func(r request) request { return lookup(ctx, s.base, r) }) {
			if l.Answered && l.Seq == answered[i].Seq && l.Accepted == answered[i].Accepted {
				cr.AnsweredKept++
			} else if lost++; lost == 1 { // report the first example, count the rest
				rep.fail("round %d: answered %s (seq %d) changed or vanished after restart: %+v", round, answered[i].Key, answered[i].Seq, l)
			}
		}
		if lost > 1 {
			rep.fail("round %d: %d answered requests in total changed or vanished", round, lost)
		}
		// 2. A refusal means the command never ran.
		for i, l := range parallel(refused, 16, func(r request) request { return lookup(ctx, s.base, r) }) {
			if l.Status == 404 && !l.Answered {
				cr.RefusedNeverRan++
			} else {
				rep.fail("round %d: refused %s nevertheless ran: %+v", round, refused[i].Key, l)
			}
		}
		// 3. A lost answer: retry with the same key. Either the command had
		// been journaled before the crash (the retry is a duplicate returning
		// the ORIGINAL seq, at or below what recovery replayed), or it never
		// was (the retry executes it now, after the recovered seq). A second
		// retry must then be a duplicate with that same seq: exactly once.
		first := parallel(unknown, 16, func(r request) request { return retry(ctx, s.base, r) })
		second := parallel(unknown, 16, func(r request) request { return retry(ctx, s.base, r) })
		for i, r1 := range first {
			r2 := second[i]
			switch {
			case !r1.Answered:
				rep.fail("round %d: retry of %s got no answer: %+v", round, unknown[i].Key, r1)
				continue
			case r1.Duplicate && r1.Seq <= rec.LastSeq:
				cr.UnknownWasDone++
			case !r1.Duplicate && r1.Seq > rec.LastSeq:
				cr.UnknownRanNow++
			default:
				rep.fail("round %d: retry of %s: duplicate=%v seq %d vs recovered seq %d", round, unknown[i].Key, r1.Duplicate, r1.Seq, rec.LastSeq)
				continue
			}
			if r2.Answered && r2.Duplicate && r2.Seq == r1.Seq {
				cr.UnknownExactOnce++
			} else {
				rep.fail("round %d: second retry of %s: %+v (first: seq %d)", round, unknown[i].Key, r2, r1.Seq)
			}
		}
		rep.Crash = append(rep.Crash, cr)
		fmt.Printf("   round %d: killed after %d ms; %d requests (%d answered, %d refused, %d unknown); recovered %d commands (torn %d B) in %s, ready in %d ms\n",
			round, cr.TrafficBeforeMs, cr.Requests, cr.Answered, cr.Refused, cr.Unknown, rec.Commands, rec.TruncatedBytes, rec.Took, cr.ReadyMs)
	}
	return nil
}

// restoreRun is one restore of a copied journal onto a fresh directory.
type restoreRun struct {
	Kind              string    `json:"kind"` // "hot" (copied while live) or "cold" (copied after the kill)
	CopiedBytes       int64     `json:"copied_bytes"`
	CopyAfterMs       int64     `json:"copy_taken_after_ms"`
	TrafficMs         int64     `json:"traffic_total_ms"`
	Original          audit     `json:"original"`
	Copy              audit     `json:"copy_offline_audit"`
	ExactPrefix       bool      `json:"copy_is_exact_prefix"`
	Restored          recovered `json:"restored_recovery"`
	PrefixDigestMatch bool      `json:"digest_equals_original_at_same_seq"`
	AnsweredKept      int       `json:"answered_up_to_restored_seq_found"`
	AnsweredLost      int       `json:"answered_after_restored_seq_lost"`
	LostWrongly       int       `json:"answered_lost_although_in_copy"`
}

func restoreDrill(rep *report, bin, work, out string, clients int, seed uint64) error {
	ctx := context.Background()
	orig := &server{bin: bin, dataDir: filepath.Join(work, "restore-data"), logPath: filepath.Join(out, "restore-original-server.log")}
	_ = os.Remove(orig.logPath)
	if _, _, err := orig.start(); err != nil {
		return err
	}
	defer orig.kill() // on every error path too; a second kill is harmless
	if _, err := fund(ctx, orig.base, "restore"); err != nil {
		return err
	}
	origWAL := filepath.Join(orig.dataDir, "journal.wal")
	hotWAL := filepath.Join(work, "restore-hot", "journal.wal")

	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan []request, 1)
	go func() { done <- traffic(tctx, orig.base, "restore", clients, seed+100) }()
	start := time.Now()
	time.Sleep(2 * time.Second)
	hotBytes, err := copyFile(hotWAL, origWAL) // a backup taken while trading continues
	if err != nil {
		return err
	}
	copiedAt := time.Since(start)
	time.Sleep(1500 * time.Millisecond)
	orig.kill()
	cancel()
	reqs := <-done
	total := time.Since(start)

	coldWAL := filepath.Join(work, "restore-cold", "journal.wal")
	coldBytes, err := copyFile(coldWAL, origWAL) // a backup taken after the crash
	if err != nil {
		return err
	}
	full, err := auditJournal(origWAL, 0)
	if err != nil {
		return err
	}

	for _, c := range []struct {
		kind, wal string
		bytes     int64
	}{{"hot", hotWAL, hotBytes}, {"cold", coldWAL, coldBytes}} {
		rr := restoreRun{Kind: c.kind, CopiedBytes: c.bytes, CopyAfterMs: copiedAt.Milliseconds(), TrafficMs: total.Milliseconds(), Original: full}
		if c.kind == "cold" {
			rr.CopyAfterMs = total.Milliseconds()
		}
		if rr.Copy, err = auditJournal(c.wal, 0); err != nil {
			return err
		}
		if rr.ExactPrefix, err = isPrefix(c.wal, origWAL, rr.Copy.ValidBytes); err != nil {
			return err
		}
		restored := &server{bin: bin, dataDir: filepath.Dir(c.wal), logPath: filepath.Join(out, "restore-"+c.kind+"-server.log")}
		_ = os.Remove(restored.logPath)
		if _, rr.Restored, err = restored.start(); err != nil {
			return err
		}
		atSeq, err := auditJournal(origWAL, rr.Restored.LastSeq)
		if err != nil {
			restored.kill()
			return err
		}
		rr.PrefixDigestMatch = atSeq.Digest == rr.Restored.Digest
		var keep []request
		for _, r := range reqs {
			if r.Answered {
				keep = append(keep, r)
			}
		}
		for i, l := range parallel(keep, 16, func(r request) request { return lookup(ctx, restored.base, r) }) {
			switch {
			case keep[i].Seq <= rr.Restored.LastSeq && l.Answered && l.Seq == keep[i].Seq:
				rr.AnsweredKept++
			case keep[i].Seq > rr.Restored.LastSeq && l.Status == 404 && !l.Answered:
				rr.AnsweredLost++ // after the copy point: lost by restoring an older backup, as expected
			default:
				rr.LostWrongly++
			}
		}
		restored.kill()
		if !rr.ExactPrefix || !rr.PrefixDigestMatch || rr.LostWrongly > 0 || rr.Copy.Invariants != "ok" || !rr.Copy.Conserved {
			rep.fail("%s restore: prefix %v, digest match %v, wrongly lost %d, invariants %s, conserved %v",
				c.kind, rr.ExactPrefix, rr.PrefixDigestMatch, rr.LostWrongly, rr.Copy.Invariants, rr.Copy.Conserved)
		}
		if c.kind == "cold" && rr.Restored.Digest != full.Digest {
			rep.fail("cold restore digest %.12s differs from the original's final digest %.12s", rr.Restored.Digest, full.Digest)
		}
		rep.Restore = append(rep.Restore, rr)
		fmt.Printf("   %s copy: %d bytes, restored to seq %d of %d (torn %d B cut), %d answers kept, %d after the copy point lost, prefix %v, digest match %v\n",
			c.kind, c.bytes, rr.Restored.LastSeq, full.LastSeq, rr.Restored.TruncatedBytes, rr.AnsweredKept, rr.AnsweredLost, rr.ExactPrefix, rr.PrefixDigestMatch)
	}
	return nil
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short=12", "HEAD").Output()
	if err != nil {
		return "unknown"
	}
	commit := strings.TrimSpace(string(out))
	if st, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output(); err == nil && len(st) > 0 {
		commit += "-dirty"
	}
	return commit
}
