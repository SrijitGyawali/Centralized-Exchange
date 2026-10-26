# 26 October 2026: reviewing week 5, then the SQL read model (week 6, part 1)

> **One-line summary:** Codex's week-5 work (HTTP API + group commit) was
> reviewed and confirmed on `main`. Then the exchange got a **PostgreSQL
> history**: a projector follows the journal and writes orders, trades and
> the ledger into SQL, without ever slowing trading down.

Plan reference: week 6. Part 1 (today): PostgreSQL projector with a
transactional checkpoint and visible lag. Part 2 (next): WebSocket depth
feed and a minimal web page. Decisions in
[ADR-0009](../../docs/adr/0009-sql-read-model-projection.md).

---

## 1. The week-5 review (what was checked)

| Area | Verdict |
|---|---|
| Group commit | ✅ Correct: Seqs for the batch → one write + one fsync → apply each → reply each; queries are barriers; a lone request never waits |
| API | ✅ Strict JSON, idempotency header, auth, per-user rate limit, deadlines; foreign/unknown orders get the same 404; fills never reveal the counterparty |
| Week-5 gate | ✅ Real binary, real HTTP, exact balances checked by hand, `Process.Kill()` restart, retries identical |
| Benchmarks | ✅ Honest: ~38× durable throughput with 64 concurrent clients |
| Minor notes | `Bearer` is case-sensitive; tokens compared by map lookup; `side` lowercase but `time_in_force` uppercase; `AppendBatch` found by a hidden type check |

The pull request had already been merged on GitHub (merge commit `cbbf0a6`),
so local `main` was just fast-forwarded. Nothing was rewritten.

---

## 2. What a "read model" is

The engine only keeps **current** state in memory: open orders, balances.
Questions about the **past** ("my last 100 trades", "every ledger line for
user 7") need a database. That database is a **projection**: a copy of the
history reshaped for queries.

```
                    the truth                      a disposable view
 Submit ─► engine ─► data/journal.wal ──follow──► projector ──► PostgreSQL
                         ▲                          │
                         └──── never the other way ─┘
```

**Golden rule:** the engine never reads SQL and never waits for it. You can
`DROP DATABASE` at any time; the projector rebuilds it from the journal,
identically.

---

## 3. How the projector stays exactly right

1. **It reads the journal, not a message stream.** It opens `journal.wal`
   read-only and resumes at a remembered byte offset (`journal.ScanFrom`).
   A message channel could drop events; the file can't.
2. **It replays every command through its OWN `state.State`.** Because
   `Apply` is deterministic, it gets exactly the effects the engine got:
   fills, outcomes and ledger journals.
3. **`plan()` turns each command into SQL statements**, with no database
   involved, so it's unit-tested.
4. **One transaction per batch (up to 256 commands):**

```
BEGIN
  SELECT seq FROM projection_checkpoint FOR UPDATE   ← lock + "is it where I expect?"
  INSERT commands / orders / trades / ledger … (one network round trip)
  UPDATE projection_checkpoint SET seq = last, last_user_id, last_key
COMMIT                                               ← rows and checkpoint appear TOGETHER
```

A crash anywhere leaves the database at a **committed checkpoint that
exactly matches its rows**. On restart, the projector replays the journal
up to that checkpoint *without writing*, then continues. Nothing is skipped
and nothing is doubled, and primary keys on journal identity make a double
write an error, not double money.

---

## 4. Lag, and why it's fine

`GET /v1/status`:

```json
{"applied_seq":"1","projection":{"enabled":true,"healthy":true,"lag":"0","projected_seq":"1"}}
```

`lag = applied_seq − projected_seq` is how stale history queries are. Some
lag is **normal by design**. The plan's signature scenario was tested for
real: **the database was frozen with `docker pause`**, orders kept
succeeding fast, the lag grew, and after unfreezing the projector caught up
to 0 with every command present **exactly once**.

---

## 5. Three real problems found today (and fixed)

| Found by | Problem | Fix |
|---|---|---|
| Real PostgreSQL (the unit tests passed!) | `$2` used twice in one SQL statement: PostgreSQL inferred `bigint` in one place and `integer` in the other, and refused | Explicit `$2::bigint` |
| A two-projector test | The second writer was stopped only by a primary-key violation, *after* starting to write | Lock and check the checkpoint **first** (`FOR UPDATE`) |
| Smoke-testing with a reused local database | A **different** journal of the same or greater length would have been silently mixed in | The checkpoint row records the command at that Seq; a mismatch is refused |

The first one is why ADR-0003 chose **testcontainers over mocks**: a mocked
database would have accepted that SQL happily.

---

## 6. Evidence

| Test | What it proves |
|---|---|
| `plan` unit tests | Every order status, maker updates, duplicates, rejections, cancels, and that IDs too big for BIGINT are refused |
| Matches the engine | 600 commands; SQL balances = replay state at the same Seq; ledger sums to 0; row counts = fills and orders |
| Growing journal | Appends between catch-ups are picked up exactly |
| Crash before COMMIT | Nothing of the failed batch is visible; a new projector finishes with zero duplicates |
| Identical rebuild | Batch size 256 vs 7: every table identical |
| Incomplete last frame | Takes 50, waits, takes the 51st once complete |
| Wrong database | Checkpoint ahead **or** a different journal: refused |
| Two writers | Second refused by the checkpoint lock |
| **Outage gate** | Real server + HTTP + `docker pause`: trading unaffected, lag grows, catches up exactly once, password never logged |

---

## 7. Go (and SQL) ideas met today

| Idea | Where | In one line |
|---|---|---|
| `//go:embed` | `migrate.go` | Files compiled into the binary: migrations can't be forgotten |
| tern migrations | `migrations/001_…sql` | Numbered SQL files, up and down; never edit an applied one |
| `pgxpool` | projector, cexd | A pool of connections shared by goroutines |
| `pgx.Batch` | `write` | Many statements in one network round trip |
| `SELECT … FOR UPDATE` | `write` | Lock a row inside a transaction; others queue |
| Build tags | `//go:build integration` | Slow Docker tests run only on request |
| testcontainers-go | integration tests | A real PostgreSQL per test run, started from Go |
| `docker pause` | outage test | Freeze a process: connections hang, nothing errors |
| Pure planning function | `plan.go` | Separate "what to write" from "writing it" to test it alone |
| Consumer interface again | `api.ProjectionStatus` | The API needs two methods; the projector provides them |
| Several `defer`s | `cexd` | Last registered runs first: projector stops before the engine drains |
| `uint64` vs `BIGINT` | `i64()` | PostgreSQL has no unsigned 64-bit type; refuse instead of wrapping |

---

## 8. Try it yourself (Docker Desktop must be running)

```bash
make test-integration                         # all PostgreSQL tests
go test -tags=integration -v -run Outage ./cmd/cexd

make db-up
CEX_DATABASE_URL=postgres://cex:cex-local-only@127.0.0.1:5433/cex \
CEX_API_TOKENS='[{"token":"my-local-admin-token","user_id":"1","admin":true}]' make run
#   in another terminal:
curl -H "Authorization: Bearer my-local-admin-token" localhost:8080/v1/status
docker exec -it deploy-postgres-1 psql -U cex -d cex -c "select * from commands"
make db-down
```

---

## 9. Check yourself

1. Why does the projector read the journal file instead of receiving events
   from the engine?
2. What exactly happens if the process dies *between* writing the rows and
   `COMMIT`?
3. Why must the checkpoint be in the same transaction as the rows?
4. Why is lag normal, and what does a *growing* lag tell an operator?
5. What did real PostgreSQL catch that the unit tests couldn't?
6. How does the projector notice it was pointed at the wrong journal?

---

## 10. Next session (week 6, part 2)

**Market data over WebSocket**: bounded-rate full depth snapshots tagged
with their Seq, one bounded buffer per subscriber, and slow subscribers
**disconnected** instead of slowing the engine. Plus a minimal web page
(book, trades, order form) showing `applied_seq` and the projection lag.
That completes the week-6 gate: "a slow browser never blocks matching."
