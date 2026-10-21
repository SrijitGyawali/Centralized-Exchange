// Package journal is the durable, append-only command log (write-ahead log).
// It is the system's source of truth: the in-memory state is rebuilt from it
// at every start, and the SQL projection (week 6) will be too.
//
// # Files
//
//	codec.go    command <-> payload bytes (canonical; strictly validated)
//	frame.go    payload  -> frame: magic, version, length, Seq, two CRC-32Cs
//	scan.go     frames   -> commands, with the recovery rules
//	journal.go  Open (recover + repair), Append (write + fsync), failpoints
//
// # Frame format (version 1, little-endian)
//
//	"CX" | version | flags | payload length | Seq | header CRC | payload | payload CRC
//	 2      1         1       4                8     4            n         4
//
// The header has its own checksum, so a damaged length can never make a
// complete frame look like a short, torn one.
//
// # Append contract
//
// Append returns nil only after the frame is written AND fsynced. The
// engine calls it before Apply, so every reply means durable and applied.
// Any write or sync error is sticky (ErrFailed): bytes may be partly on
// disk, so only a restart that re-reads the file may continue.
//
// # Recovery rules (ADR-0006)
//
//   - A file that ends exactly after a frame: clean.
//   - A file that ends INSIDE the last frame (a crash cut the write short):
//     a torn tail. Open truncates it; that command was never acknowledged.
//   - A complete frame that fails any check (magic, either checksum,
//     version, flags, length, Seq continuity, payload decoding): stop with
//     ErrCorrupt or ErrUnsupportedVersion, and leave the file untouched.
//     Never skip.
//
// # Failure model
//
// Tier 1 targets PROCESS crashes on a persistent local disk (tested by
// tests/crash). Power loss, disk loss and machine loss are out of scope for
// now and documented as limitations. The checksums detect accidental damage;
// they do not authenticate the file.
package journal
