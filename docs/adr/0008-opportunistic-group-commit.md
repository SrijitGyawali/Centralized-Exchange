# ADR-0008: Opportunistic group commit

- **Status:** Accepted
- **Date:** 2026-10-23

## Context

Week 4 called Write and Sync for every command. Sync dominates the durable
path on many disks. With concurrent clients, commands are already waiting in
the bounded engine queue; sharing a sync can amortize that cost without
weakening acknowledgement guarantees or introducing a batching timer.

## Decision

After dequeuing a command, the owner nonblockingly drains immediately available
commands, up to `BatchSize` (default 64, environment range 1–1024). An empty
queue ends collection immediately. A single request never waits for another
request to fill its batch. A query is held as a barrier: preceding commands
finish before the query, and following commands cannot jump ahead of it.
BatchSize 1 provides the one-sync-per-command comparison mode.

The owner assigns increasing sequences in dequeue order and calls
`Journal.AppendBatch([]command.Command)`. Storage validates all sequences and
encodes all existing-format frames before doing I/O. With production hooks
unset, the contiguous buffer uses one Write and one Sync. Only after Sync
succeeds does the engine Apply commands in order, publish each snapshot and
send each buffered reply. A caller abandoning its wait cannot stop an admitted
command, stall replies or cancel the remaining batch.

`Append` delegates to a one-element batch. The engine recognizes batch-capable
storage through an optional interface; existing Append-only test/alternate
writers retain their one-command durable apply path. This keeps week-4 test
doubles meaningful without pretending that separate appends share a sync.

### Errors and crash recovery

A validation/encoding error writes nothing and does not poison the journal.
A short write, write error or sync error is sticky: later appends fail until
restart. The engine halts without applying any commands from a failed batch;
callers get an unknown outcome, never success. Recovery remains authoritative
because fully or partially written bytes may already be on disk.

Batches are not transactions. Each frame remains individually checksummed and
sequenced in the week-4 WAL format. A crash can leave a complete prefix and a
torn tail. Recovery replays the prefix and repairs only the torn tail using
the existing policy. It may recover commands never acknowledged, so retries
must keep their original per-user key. No acknowledged command depends on a
later unsynced batch member.

Crash hooks intentionally use per-frame writes, including half-frame writes,
to retain deterministic crash positions. All AfterSync hooks run after the
single shared Sync; AfterApply hooks report the individual command's sequence.
The normal one-write guarantee excludes this test-only instrumentation.

Closing intake drains any collected batch and queued work. Halting keeps the
process alive for readiness/error reporting while refusing new trading work.

## Alternatives and consequences

- A timer could build larger batches under light load but deliberately adds
  latency to isolated requests. We choose only already-queued work.
- Applying before Sync could reduce apparent response time but violates the
  durable-acknowledgement contract. It is excluded.
- Atomic multi-command records would change the recovery format and semantics.
  Retaining ordinary frames preserves existing journals and replay tools.

Batching adds a bounded buffer and can make early requests wait for the shared
write containing later requests. Queries may split batches. Throughput gains
depend on queue depth, storage and workload; no gain is promised for serial
clients. Benchmarks compare real storage with BatchSize 1 and 64, report units
per command, and distinguish journal-only amortization from end-to-end engine
throughput. Measurements live in `evidence/bench/2026-10-23-group-commit.txt`.
