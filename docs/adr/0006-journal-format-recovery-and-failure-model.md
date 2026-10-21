# ADR-0006: Journal format, recovery policy and failure model

- **Status:** Accepted
- **Date:** 2026-10-21

## Context

ADR-0001 made the command log the source of truth, and ADR-0005 promised
that a reply will mean "durable and applied". We now need the on-disk
format, the exact rules for reading it back after a crash, and an honest
statement of which failures are covered.

The hard part is telling apart two things that look alike on disk:

- **A torn tail:** the process died while writing the *last* frame. That
  command was never acknowledged, so dropping it is correct.
- **Corruption:** a *complete* frame is wrong. Skipping it would silently
  lose or reorder commands, and with them, money.

## Options considered

1. **PostgreSQL as the log.** Mature durability, but it's another moving
   part on the hot path, and the project's learning goal is the journal
   itself (plan §04). Kept as an alternative design. ❌
2. **JSON lines.** Human-readable, but not canonical (field order,
   whitespace, number formatting), slower to parse strictly, and a torn
   line is ambiguous. ❌
3. **Length-prefixed binary frames with checksums** ✅
4. **One checksum per frame** vs **separate header and payload
   checksums** ✅. With one checksum, a bit flip in the *length* of the
   last frame makes it look torn, and it would be silently truncated. A
   header checksum lets the length be trusted before the payload is read.

## Decision

### Format (version 1)

```
"CX" | version | flags | payload length | Seq | header CRC | payload | payload CRC
 2      1         1       4                8     4            n ≤ 256   4
```

- Little-endian; CRC-32C (Castagnoli). The checksums detect accidental
  damage; they are not authentication.
- **Payload:** a canonical varint encoding of user, key and body
  (`codec.go`). Values are recorded *exactly as sent*, including invalid
  ones, so replay reproduces the same rejections. Golden-bytes tests lock
  the format; changing it requires a new version number.
- Every **sequenced** command is journaled: accepted, rejected and
  duplicates alike. Queries are not. Invalid keys and nil bodies are
  refused before admission and never reach the file.
- One file, `DataDir/journal.wal`. Segments and rotation come with
  snapshots in Tier 2.

### Append

- The engine appends **after** assigning Seq and **before** `Apply`.
  Append = write the whole frame, then `File.Sync` (fsync /
  FlushFileBuffers). Only then may the command change state or be
  acknowledged.
- Any write or sync error is **sticky**. The journal refuses further
  appends and the engine halts. A restart re-reads the file and learns
  what actually landed. The caller who was waiting gets
  `ErrOutcomeUnknown`.
- On POSIX systems the directory is fsynced after opening, so a new
  file's name is durable too. Windows has no such call; NTFS journals its
  metadata.

### Recovery (on every start: `node.Open`)

| What `Scan` finds | Action |
|---|---|
| File ends exactly after a frame | Clean |
| File ends inside the last frame, after a valid-looking prefix | **Torn tail:** truncate to the last complete frame, fsync, continue |
| Bad magic, bad header or payload CRC, unknown flags, length > 256, Seq not exactly previous+1 (starting at 1), undecodable payload | **ErrCorrupt:** refuse to start, file untouched |
| Valid frame with a different version | **ErrUnsupportedVersion:** refuse to start |
| A recovered command makes `Apply` fail or panic, or the recovered state breaks an invariant | Refuse to start |

Then every command is replayed through the same `state.Apply`, all
invariants are checked, and the state digest (SHA-256 of the canonical
state) is logged. The `replay` tool computes the same digest from the file
without modifying it.

### Failure model: what is claimed

- ✅ **Process crash** (killed, panicked, `os.Exit`) on a persistent local
  disk: every acknowledged command survives exactly once; an in-flight
  command is either fully present or absent, and a retry with its key
  resolves which. **Tested** by `tests/crash` at five boundaries (before
  write, half-written, after write before fsync, after fsync before
  apply, after apply before reply), at three positions, plus repeated
  crashes.
- ⚠️ **"After write, before fsync" survives a process crash only because
  the OS cache survives.** Power loss in that window could lose it, which
  is fine, because it was never acknowledged.
- ❌ **Not claimed:** power loss (not tested; depends on the device
  honouring flushes, and laptop write caches may not), disk or machine
  loss (needs backups and replication: Tier 2 and Tier 3), malicious
  edits, and bugs that write wrong-but-valid frames.

## Consequences

- **Easier:** recovery is one code path, used in production, tests and
  the replay tool. Crash safety is demonstrated, not assumed.
- **Harder:** every command pays an fsync, measured at about 1.4 ms on the
  dev laptop, roughly 500× the engine's in-memory work
  (`evidence/bench/2026-10-21-durability-cost.txt`). That caps a single
  writer at around 700 durable commands per second; **group commit**
  (one fsync per batch) is the planned answer in week 5. The file grows
  forever until snapshots and rotation arrive (Tier 2). Recovery time
  grows with history, and will be measured at 100k and 1M commands.
- **Evidence:** exhaustive torn-tail tests (every cut point) and
  corruption tests (every flipped byte); fuzzing of the decoder and the
  scanner (~1.6M inputs each, no failures); golden bytes; the crash
  harness (20 child processes); and a deliberately broken engine
  (reply before journal) that the harness caught immediately.
