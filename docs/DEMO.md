# The three-minute demonstration

One command runs it, narrated, against the real binary:

```bash
make demo          # or: go run ./tests/demo   (about 15 seconds)
```

It builds `cexd` and the replay inspector, starts the server on a fresh
data directory, and prints each act. A recorded run is in
[`evidence/demo/`](../evidence/demo/). CI runs it on every push, so it
cannot silently stop working: the program fails if the retry after the
crash is not the original result.

Below: what to say while each part is on screen. Timings are a guide.

---

## 0:00-0:30 · Scope

> "This is a simulated BTC/USDT spot exchange in Go. One goroutine owns
> all trading state, so there are no locks around the book or the
> balances. Every command is written to a journal and fsynced *before* it
> is applied and answered, so an answer means it survives a crash. Money is
> exact integers in a double-entry ledger. It is paper trading only."

## 0:30-1:15 · Matching, explained by the inspector

On screen: bob and carol each sell 0.1 BTC at 60000 (bob first), bob sells
0.1 at 59900, then alice buys 0.25 with a limit of 60050. Then the output
of `replay -explain <alice's order>`.

> "Price first: the 59900 ask fills before the 60000 ones. Then time: at
> 60000, bob's order is older than carol's, so it fills first, and carol
> keeps half of hers. Every fill is at the *maker's* price, so alice pays
> less than her limit. She reserved 15,012.50 USDT up front; the fills cost
> 14,990, and the release at the end returns the 22.50 she did not need.
> Each line of the ledger sums to zero per asset. And this explanation is
> recomputed from the journal by the same state machine the server ran:
> no logs, no database."

## 1:15-2:00 · A lost response, a crash, a retry

On screen: alice places another order; we pretend the reply was lost; the
server is killed with `kill -9`; it restarts and replays the journal; alice
retries with the same key.

> "Alice's reply was lost on the network: she does not know whether her
> order exists. The server is killed hard. On restart it replays the journal
> and comes back with the same state, same digest. Alice retries with the
> *same idempotency key*: she gets `duplicate: true` with the original
> sequence number and order ID. Her reserved balance shows one order, not
> two. Retrying is always safe; a timeout never rolls anything back."

The drills prove the same at scale: five kills mid-traffic, every answered
request still there, every lost answer retried with exactly one effect
([report](../evidence/crash/2026-10-28/report.md)).

## 2:00-2:40 · Where the time goes

On screen: a 400-order burst, then the server's own metrics: HTTP, engine
total, queue wait, durable (write + fsync, per batch), apply, and commands
per fsync.

> "Apply, the matching engine itself, takes a few microseconds. The
> durable step, write plus fsync, takes over a millisecond, and the engine
> waits for it: that is the cost of 'acknowledged means durable'. Group
> commit is the one optimization that matters: queued commands share one
> fsync, so under load the engine serves 3200 orders a second at a 2.4 ms
> median on this laptop, where one fsync per order saturates near 750. The
> workload, the errors and the raw results are in the performance report."

Point to [PERFORMANCE.md](PERFORMANCE.md): result 1 (group commit vs
fsync per order) and result 2 (the latency breakdown).

## 2:40-3:00 · Limits, and the next decision

> "It is one process with one journal on one disk, so a machine loss needs
> replication, which is not built. The latency tail is the disk's rare slow
> fsyncs. Memory grows by about 370 bytes per command because every
> idempotency key is kept forever. With more load I would add snapshots and
> dedupe expiry first; for availability, a follower replaying the same
> journal, which the deterministic state machine makes possible."

Point to [LIMITATIONS.md](LIMITATIONS.md).

---

## Likely questions

| Question | Answer, and the evidence |
|---|---|
| Why a single writer? | Atomic state ownership and replay by construction; a throughput ceiling that was measured, not assumed ([ADR-0001](adr/0001-scope-and-single-writer.md), [PERFORMANCE](PERFORMANCE.md)) |
| Is it lock-free? | No claim of formal lock-freedom. Channels synchronize internally; the book and balances need no application mutex because one goroutine owns them |
| What if a success response is lost? | The result is stored durably under (user, key); the same-key retry returns it, including after a restart ([drills](../evidence/crash/2026-10-28/report.md), the demo) |
| How do funds stay correct? | Every journal balances per asset; reservations cover every open order; invariants are checked at startup and in every drill round; issued = held is checked independently ([ADR-0004](adr/0004-accounting-reservations-ledger-idempotency.md)) |
| How would you scale it? | Measure first: the bottleneck is fsync, not CPU (~93 % of the engine's time). Faster storage, then more symbols under the writer; replication for availability; sharding is a separate consistency problem |
| What did Go teach you? | The Windows clock that ticked every 578 µs ([PERFORMANCE](PERFORMANCE.md)); a typed nil in an interface; ServeMux pattern conflicts; `atomic.Value` panicking on mixed error types |
| What remains unsafe? | Power loss untested, machine loss = data loss to the last copy, unbounded dedupe memory, static tokens, no TLS ([LIMITATIONS](LIMITATIONS.md)) |
| How do you know the tests test anything? | Reference model for the book, fuzzing of parsers and the journal decoder, a crash harness with failpoints at every write boundary, and drills against the real binary |
