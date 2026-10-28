package main

import (
	"fmt"
	"strings"
)

// markdown renders report.md from the results. Every claim in it is a count
// the drill checked; "Failures" lists any check that did not hold.
func markdown(r *report) string {
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	p("# Drills: crash, retry, money, restore, recovery time\n\n")
	p("| | |\n|---|---|\n| started (UTC) | %s |\n| git commit | `%s` |\n| machine | %s, %d logical CPUs, %s |\n", r.StartedAt, r.Commit, r.OS, r.CPUs, r.Go)
	p("| settings | %d kill rounds, %d clients over %d users, seed %d, recovery sizes [%s] |\n\n",
		r.Settings.CrashRounds, r.Settings.Clients, r.Settings.Users, r.Settings.Seed, r.Settings.Sizes)
	p("Every stop below is a hard kill (`Process.Kill`: SIGKILL on Unix, TerminateProcess on Windows) of the real `cexd` binary while %d clients trade. ",
		r.Settings.Clients)
	p("This is the PROCESS-crash model: the operating system survives, so data written but not fsynced may survive too. ")
	p("Power loss and machine loss are not tested here (see docs/LIMITATIONS.md).\n\n")

	if len(r.Failures) == 0 {
		p("**Result: every check passed.**\n\n")
	} else {
		p("**Result: %d check(s) FAILED** (listed at the end).\n\n", len(r.Failures))
	}

	p("## 1. Crash under load, then retry\n\n")
	p("Answered = the client got a definite result. Refused = 503 before admission (never ran). Unknown = no answer (killed in flight): retried with the same key.\n\n")
	p("| round | killed after | requests | answered | refused | unknown | recovered commands | torn tail cut | ready after restart | answers kept | refused never ran | unknown: already done | unknown: ran on retry | exactly once |\n")
	p("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, c := range r.Crash {
		p("| %d | %d ms | %d | %d | %d | %d | %d (in %s) | %d B | %d ms | %d / %d | %d / %d | %d | %d | %d / %d |\n",
			c.Round, c.TrafficBeforeMs, c.Requests, c.Answered, c.Refused, c.Unknown, c.Restart.Commands, c.Restart.Took,
			c.Restart.TruncatedBytes, c.ReadyMs, c.AnsweredKept, c.Answered, c.RefusedNeverRan, c.Refused,
			c.UnknownWasDone, c.UnknownRanNow, c.UnknownExactOnce, c.Unknown)
	}
	p("\n\"Unknown: already done\" are LOST RESPONSES: the command was journaled and executed, the client never heard back, ")
	p("and the retry returned the original result instead of running it twice. ")
	p("A torn tail is rare here: a kill must land inside one write system call, against ~1 ms spent in fsync; ")
	p("torn tails and corrupt frames are covered deterministically by tests/crash and the journal tests.\n")
	p("\nMoney checks after every kill (offline replay of the journal, before the restart):\n\n")
	p("| round | last seq | invariants | issued = held (USDT, BTC) | digest = what the restart recovered |\n|---|---|---|---|---|\n")
	for _, c := range r.Crash {
		p("| %d | %d | %s | %v | %v (`%.16s…`) |\n", c.Round, c.Offline.LastSeq, c.Offline.Invariants, c.Offline.Conserved,
			c.Offline.Digest == c.Restart.Digest, c.Offline.Digest)
	}

	p("\n## 2. Restore from a copied journal\n\n")
	p("A hot copy is taken while trading continues (what a naive file backup of a running server gets); a cold copy after the kill. ")
	p("Each is restored on a fresh directory by starting `cexd` on it.\n\n")
	p("| copy | copied after | bytes | restored to seq | original last seq | torn tail cut | exact prefix of the original | digest = original at that seq | answers kept | answers after the copy point (lost) | wrongly lost |\n")
	p("|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, x := range r.Restore {
		p("| %s | %d ms of %d | %d | %d | %d | %d B | %v | %v | %d | %d | %d |\n", x.Kind, x.CopyAfterMs, x.TrafficMs, x.CopiedBytes,
			x.Restored.LastSeq, x.Original.LastSeq, x.Restored.TruncatedBytes, x.ExactPrefix, x.PrefixDigestMatch, x.AnsweredKept, x.AnsweredLost, x.LostWrongly)
	}
	p("\nA restore loses exactly what happened after the copy was taken (the recovery point), and nothing before it.\n")

	if len(r.Recovery) > 0 {
		p("\n## 3. Recovery time\n\n")
		p("`node.Open` on a journal written through the real engine (group commit, mixed orders and cancels from %d users): scan, both CRCs, `Apply`, every invariant, the digest. Three runs each.\n\n", users)
		p("| commands | journal | written in | recovery (3 runs) | median | replay rate | heap after recovery |\n|---|---|---|---|---|---|---|\n")
		for _, x := range r.Recovery {
			p("| %d | %.1f MiB | %.1f s | %s | **%s** | %.0f commands/s | %.0f MiB |\n", x.Commands, float64(x.JournalBytes)/(1<<20),
				x.GenerateSeconds, strings.Join(x.Recoveries, ", "), x.RecoveryMedian, x.CommandsPerSec, x.HeapMiB)
		}
	}

	if len(r.Failures) > 0 {
		p("\n## Failures\n\n")
		for _, f := range r.Failures {
			p("- %s\n", f)
		}
	}
	p("\n## Raw files\n\n- `results.json`: every number above\n- `crash-server.log`, `restore-*-server.log`: the servers' own JSON logs, including each `recovered` line\n")
	p("\nReproduce: `go run ./tests/drills -out <dir>` (about 5 minutes; `-crash-rounds 1 -sizes \"\"` for a quick run).\n")
	return b.String()
}
