# 28 October 2026: freeze and demonstrate (week 8, v0.1.0)

> **One-line summary:** Tier 1 is done and released as **v0.1.0**. The
> replay inspector now explains any order from request to ledger; drills
> kill the real server mid-traffic and prove nothing acknowledged is lost
> and no retry runs twice; a one-command demo tells the story; and the
> limits are written down next to the evidence.

Plan reference: week 8, "freeze and demonstrate". Gate: *tag v0.1 with test
reports, measured results, a demo recording, and a clear Tier 1 capability
list.* Where each item lives: [docs/RELEASE.md](../../docs/RELEASE.md).

---

## 1. The inspector: "why did my order fill like that?"

`replay -explain <order>` replays the journal through the same `state.Apply`
the server ran and prints the order's whole life:

```
request    seq 7, user 1, key "b1": buy 0.2500 BTC limit 60050, GTC
reserve    15012.500000 USDT at the limit price
matching   3 fill(s), in priority order (best price first; at one price, the oldest order first):
  1. 0.1000 BTC @ 59900 against order 3 (user 2, resting since seq 6), 150 USDT better than the limit
  2. 0.1000 BTC @ 60000 against order 1 (user 2, resting since seq 4), 50 USDT better than the limit
  3. 0.0500 BTC @ 60000 against order 2 (user 3, resting since seq 5), 50 USDT better than the limit
ledger     seq 7 #0 reservation … #1-#3 settlement … #4 release (the 22.50 not needed)
```

Each rule shows up as data. **Price**: 59900 fills first. **Time**: at 60000
the seq-4 order beats the seq-5 one. **Maker price**: every fill is at the
resting order's price. **Reservation**: 15,012.50 held, 14,990 spent, 22.50
released. The explanation comes from the journal alone, not from logs or
SQL, so it is exactly what the exchange decided. `-explain-seq` does the
same for one command, including a retry: "DUPLICATE of seq 7, nothing
executed, no postings".

## 2. The drills: proving the promises against the real binary

`go run ./tests/drills` (report: [evidence/crash/2026-10-28](../../evidence/crash/2026-10-28/report.md)):

| Drill | What was checked | Result |
|---|---|---|
| Crash ×5 | 32 clients trade; the server is **killed** (SIGKILL / TerminateProcess) at a random moment; restart | 129,063 / 129,063 answered requests still there, same Seq, same decision |
| Lost answers | Every request with no answer is retried twice with its key | 233 / 233 exactly once: 84 had run before the crash (lost responses: the retry returned the original), 149 ran on the retry |
| Money | Offline replay after every kill: invariants, digest, issued = held | All 5 rounds: ok, conserved, digest = what the restart recovered |
| Restore (hot) | Journal copied while trading continued, restored on a fresh dir | Exact byte prefix; digest = the original's at seq 16,393; lost only the 14,323 answers after the copy point |
| Restore (cold) | Copied after the kill | Recovered the final digest; lost nothing |
| Recovery time | `node.Open` on journals written through the real engine | 100k commands: 1.2 s; **1M: 8.0 s** (42.5 MiB, 432 MiB heap) |

Three things this taught:

1. **"Unknown" splits in two.** A request whose answer was lost either
   ran (it reached the journal before the kill) or did not. The client
   cannot tell, and does not need to: the same-key retry returns the
   original in the first case and runs it in the second. That is the
   whole point of idempotency keys, seen 233 times.
2. **A file copy of a live journal is a valid backup**, because the
   journal is append-only and every frame is checksummed: the copy is
   always a clean prefix (at worst with a torn last frame, which recovery
   cuts off). Its **recovery point** is the moment of the copy.
3. **Recovery time is now a number.** 8 s per million commands with no
   snapshots. At 100 orders/s that is about 3 hours of trading per 8 s of
   startup, which tells you when snapshots become necessary.

## 3. The demo as a program

`make demo` runs the plan's three-minute story against the real binary:
scope → matching explained by the inspector → a lost response, a hard kill,
a retry with one effect → a 400-order burst and the server's own latency
split (apply 4 µs, durable 1.35 ms, 7.2 commands per fsync) → the limits.
It **fails** if the retry is not the original result, and CI runs it on
every push, so the demo cannot silently stop working. The spoken script and
likely questions: [docs/DEMO.md](../../docs/DEMO.md).

Small things it found: the per-user rate limiter (100/s) correctly refused
the demo's burst (429), so the demo raises it as the load tests do; and the
first "where the time goes" act measured one command (metrics restart with
the process), so a burst comes first.

## 4. Writing down the limits

[docs/LIMITATIONS.md](../../docs/LIMITATIONS.md) is organized by the plan's
interview question, *what remains unsafe?*:

- **Machine loss**: one journal on one disk, so data is lost back to the
  last copy. Power loss is **untested** (a process kill leaves the OS and its
  page cache alive).
- **Software bugs**: a deterministic bug is replayed faithfully. The
  defences are invariants at startup, halting on panic, and the reference
  model.
- **Corruption**: startup refuses and a human investigates; there is no
  repair tool.
- **Operational limits**: unbounded dedupe memory (~370 B per command),
  the fsync tail, static tokens, no TLS, the projector's blindness to a hung
  database.

Each row names its evidence and what would lift it. Writing it changed two
numbers: the journal is ~45 bytes per command, not the "50-100" first
guessed.

## 5. A clean machine, and the release checks

The quick start was run on a **clean machine**: a fresh `golang:1.26.10`
container (Debian, with only Go, git and make), cloning the repository from
GitHub. `make check` (gofmt, vet, race tests), `make run` answering
`ok` / `ready`, `make demo`, the quick drills and the week-5 gate all
passed ([transcript](../../evidence/quickstart/2026-10-28-clean-container.txt)).

Before the push, all 20 week-8 commits were checked one by one (all pass:
the drill entry point was committed first this time). One hiccup: the
PostgreSQL integration tests failed with "is Docker running?". Docker
Desktop had been closed since yesterday. Started again, they passed. The
message said exactly what was wrong, which is why it was written that way.

## 6. Revisiting every claim

The plan asks to "remove anything the evidence does not support". The
README now lists each core guarantee with the test or drill that proves it;
"power loss" and "machine loss" are explicitly not claimed; the demo
recording is described honestly as a transcript, not a video.

## 7. Go ideas met today

| Idea | Where | In one line |
|---|---|---|
| A method value as a callback | `tracer.apply` | The replay's per-command hook carries its own state |
| Type switch on a sealed interface | `describe` | Every command kind rendered; the compiler knows the set |
| `exec.Cmd` + `Process.Kill` | drills, demo | A real crash, not a simulated one |
| `sync.WaitGroup.Go` | drills | Go 1.25: start and track a goroutine in one call |
| Bounded worker pool over a channel | `parallel` | 16 workers check 30k lookups without 30k goroutines |
| A mutex for "first error" | `recovery.go` | `atomic.Value` panics on mixed concrete types |
| `go vet` lostcancel | restore drill | It caught a context (and a server) left running on an error path |
| Entry-point-first commits | `tests/drills` | A main package must link: last week's lesson, applied |

## 8. Check yourself

1. What does `-explain` print that proves *time* priority, not just price?
2. Why can the drill not tell, before retrying, whether a lost request ran?
   Why does it not matter?
3. Why is a hot file copy of the journal a valid backup, and what is its
   recovery point?
4. What does "recovers 1M commands in 8 s" imply for when snapshots are
   needed?
5. Which failure is *not* covered by a process kill, and why?
6. Why does the demo fail on purpose when the retry is not a duplicate?

## 9. What comes next (Tier 2)

From the limits, in order: snapshots and journal segments (recovery time,
disk), dedupe expiry with replayed expiry commands (memory), a projector
statement timeout, then a follower replaying the same journal
(availability). And: start interviewing with v0.1.0.
