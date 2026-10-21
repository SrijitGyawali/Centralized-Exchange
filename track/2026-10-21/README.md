# 21 October 2026: saving to disk (the write-ahead log)

> **One-line summary:** every command is now written to a file and forced
> onto the disk **before** it changes anything. On every start the
> exchange rebuilds itself by replaying that file. A crash harness kills
> the process at five different moments and proves no acknowledged command
> is ever lost or doubled.

Plan reference: week 4. **Week 4 gate ✅:** "a subprocess crash harness
confirms acknowledged effects survive, unknown outcomes resolve by key,
and restored full state matches the recovered log." Decisions recorded in
[ADR-0006](../../docs/adr/0006-journal-format-recovery-and-failure-model.md).

---

## 1. The idea in one picture

```
 Submit ─► engine: Seq++ ─► journal: write + fsync ─► state.Apply ─► reply
                                    │
                                    ▼
                         data/journal.wal   (append-only, the source of truth)

 restart ─► scan the file ─► cut off a half-written last frame (if any)
         ─► replay every command through state.Apply
         ─► check all invariants ─► fingerprint (digest) ─► ready
```

**Write-ahead** = write it down *before* doing it. If the process dies at
any moment, the file says exactly what happened, and replaying it rebuilds
exactly the same state, because `Apply` is deterministic (week 3).

---

## 2. Write vs fsync (the most important idea today)

| Call | What it really does | Survives a process crash? | Survives power loss? |
|---|---|---|---|
| `f.Write(b)` | Copies bytes into the **operating system's cache** | ✅ yes (the OS still has them) | ❌ maybe not |
| `f.Sync()` | Waits until the **device** says the bytes are stored | ✅ yes | ✅ (if the device is honest) |

`Sync` is slow, about **1.4 ms** on this laptop versus about 2.7 µs for
everything else the engine does. That's roughly **500×**. It's still not
optional: a reply is a promise, and the promise is only true after `Sync`.

---

## 3. What a frame looks like on disk

```
 "CX" │ ver │ flags │ length │   Seq    │ header CRC │ payload (≤256) │ payload CRC
  2B     1B    1B      4B        8B          4B             n             4B
```

| Part | Why it's there |
|---|---|
| **magic `CX`** | Recognise our frames; foreign bytes are rejected |
| **version** | Old files stay readable when the format changes later |
| **length** | Know how many bytes to read, checked against 256 *before* reading |
| **Seq** | Must be 1, 2, 3 … with no gap |
| **header CRC** | Trust the length *before* reading the payload (see §4) |
| **payload** | The command: canonical varints (`codec.go`) |
| **payload CRC** | Detect any damaged byte in the command |

**Canonical encoding:** one command → exactly one byte string. The decoder
rejects anything else (padded varints, trailing bytes). The fuzzer checks
decode → encode → identical bytes.

**Golden bytes:** `TestGoldenEncoding` pins the exact hex. For example,
`0101026162070280897a` is "fund user 7 with 1 USDT from user 1, key `ab`".
I worked it out on paper first, including the zigzag varint `80 89 7a` for
2,000,000. If it ever changes, old journals become unreadable, so the test
forces a deliberate version bump.

---

## 4. Torn tail vs corruption (the hardest part)

Two situations look alike on disk:

| | Torn tail | Corruption |
|---|---|---|
| What happened | The process died **while writing the last frame** | A **complete** frame is wrong (bit flip, bad edit) |
| What's on disk | A *correct prefix* of a frame, then the file ends | All the bytes, but a check fails |
| Was it acknowledged? | **Never** (acks wait for fsync) | Possibly |
| What we do | **Cut it off**, continue | **Refuse to start**, leave the file untouched |

**Why two checksums?** Imagine one bit flips in the *length* of the last
frame, making it claim to be longer than the file. With a single checksum
at the end, that complete frame would look "torn" and be **silently
deleted**. The header checksum catches the damaged length first.
`TestOversizedLengthIsCorruptNotTorn` checks exactly this case.

---

## 5. The crash harness (how we *prove* it)

```
 parent test ──exec──► child = the same test binary (TestCrashChild)
                         opens a node, sends a 10-step script,
                         prints "ACK n {result}" for every reply,
                         os.Exit(77) at the chosen moment ← "killed"
 parent ◄── ACK lines ──┘
 parent: recover from the same directory, then check everything
```

**Five crash points** inside one command's life:

| Point | On disk when it dies | In-flight command after restart |
|---|---|---|
| `before-write` | nothing | absent → retry **runs it** |
| `mid-write` | half a frame | torn tail cut off → retry **runs it** |
| `after-write` | whole frame, not fsynced (OS cache) | present → retry is a **duplicate** |
| `after-sync` | durable, not applied | present → **duplicate** |
| `after-apply` | applied, reply not sent | present → **duplicate** |

Each point is tested at steps 1, 4 and 9 (15 crashes), plus a clean
restart and a **chain of 4 crashes** in one directory. After each, the
parent proves:

1. every **acknowledged** step survived exactly once (its retry is a
   duplicate with the same result);
2. the **in-flight** step resolves by its key, exactly as the table says;
3. **every** outcome equals a crash-free run of the same script;
4. the final state's **digest equals an independent replay** of the
   journal file, and equals what a fresh restart recovers.

### Testing the tester

I broke the engine on purpose so it **replied before writing to the
journal**, then ran the harness. It failed immediately:
`crashed=true after 1 ACKs; want a crash with 0 ACKs`. A client was told
"done" about a command that was then lost. The engine was restored from a
verified backup.

---

## 6. Other things built today

| Piece | What it does |
|---|---|
| `state.Digest()` | SHA-256 of the *entire* state in canonical (sorted) order. Two states are equal exactly when their digests are, so recovery checks become one comparison. It even sees FIFO order that balances and depth can't. |
| `node.Open` | The one recovery path used by `cexd`, the crash harness and the tests |
| Engine + journal | `Journal` interface (one method, declared by the engine), append before Apply, halt on any storage error |
| `cmd/replay` | Read-only inspector: frames, torn tail, invariants, digest, balances, book, `-to-seq N` |
| `cexd` | Recovers on start, logs the digest, `/readyz` → 200, shuts down HTTP → engine → journal |

---

## 7. Evidence summary

| Evidence | Result |
|---|---|
| 🔴 Red step | Journal tests failed against stubs (one even panicked) |
| ✂️ Torn tail at **every** byte of the last frame | Kept exactly the complete frames, truncated, appended again |
| 🔨 Corruption at **every** byte of a 3-frame file | Every flip refused; nothing at or after the damage applied |
| 🎲 Fuzzing | ~1.6M inputs each for the decoder and the scanner, no failures |
| 💥 Crash harness | 20 child processes killed or stopped; all four proofs hold |
| 🧪 Sabotaged engine | Harness caught "reply before journal" at once |
| 🔍 Real binaries | `cexd` ready, restart recovered, `replay` digest = server digest |
| ⏱ Benchmarks | encode 35–63 ns; append+fsync ~1.4 ms; durable submit ~1.4–1.9 ms |

---

## 8. Go ideas met today

| Idea | Where | In one line |
|---|---|---|
| `encoding/binary` varints | `codec.go` | `AppendUvarint`/`AppendVarint`; zigzag keeps small negatives small |
| `hash/crc32` Castagnoli | `frame.go` | Fast hardware-accelerated checksum |
| `io.ReadFull`, `io.ErrUnexpectedEOF` | `scan.go` | Tell "clean end" from "cut short" |
| `bufio.Reader` | `scan.go` | Fewer system calls while scanning |
| `os.File.Sync`, `Truncate`, `Seek` | `journal.go` | Durability and repair |
| `runtime.GOOS` | `syncDir` | Platform-specific behaviour without build tags |
| Sticky errors | `Journal.err` | After an I/O failure, refuse everything until restart |
| Failpoints (hooks) | `journal.Hooks`, `engine.Hooks` | Test-only crash points in production code, nil by default |
| Re-exec the test binary | `tests/crash` | `exec.Command(os.Args[0], "-test.run=...")` to test process death |
| `os.Exit` skips defers | crash child | Exactly like being killed: nothing gets cleaned up |
| `crypto/sha256` + sorted maps | `digest.go` | Deterministic fingerprints need deterministic order |
| `testing.B.Loop` | benchmarks | Go 1.24+ benchmark loop |
| `defer` order | `cexd` | Registered first, so it runs last: HTTP stops before the engine drains |

---

## 9. Try it yourself

```bash
go test -v ./tests/crash                          # watch 20 processes die and recover
go test -run TestTorn -v ./internal/journal
go test -run '^$' -fuzz=FuzzScan -fuzztime=30s ./internal/journal
go test -run '^$' -bench AppendSync -benchtime=2000x ./internal/journal   # your disk's fsync

make run              # start the server; Ctrl-C; start again: see "recovered"
go run ./cmd/replay -data-dir ./data
```

---

## 10. Check yourself

1. Why must the journal append happen *before* `Apply` and not after?
2. A crash happens after `Write` but before `Sync`. Is the command there
   after restart? Would it be after a power cut?
3. Why does a damaged *length* need its own checksum to be caught?
4. Why does recovery truncate a torn tail but refuse to start on a corrupt
   frame?
5. Why are rejected commands and duplicates written to the journal too?
6. What do the five crash points each prove?
7. Why is `os.Exit` a good stand-in for "the process was killed"?

---

## 11. Next session (week 5)

**The HTTP API** (place, cancel, status, balances, book; token auth;
ownership checks; status codes from ADR-0005) and **group commit**: batch
several commands into one fsync, so the 1.4 ms is shared instead of paid
by every command.
