// Package journal is the durable, append-only command log (write-ahead log).
// It is the system's source of truth: the in-memory state and the SQL
// projection are both rebuilt from it.
//
// # Frame format (week 4)
//
//	magic | format version | payload length | seq | payload | checksum
//
// The length is capped before allocation, so a corrupt length cannot exhaust
// memory. The checksum detects accidental corruption; it does not
// authenticate.
//
// # Append contract
//
//   - Append returns success only after the bytes are written *and* Sync has
//     returned. Short writes and every error are handled.
//   - Any append or Sync error is fatal for trading. Some bytes may have
//     reached disk even though we saw an error, so we stop, restart and let
//     replay plus dedupe resolve the outcome.
//   - Group commit (week 5): batch several commands into one Sync. This
//     amortizes the cost of Sync but adds batch wait to latency.
//
// # Recovery contract
//
//   - Replay every complete, valid frame in order.
//   - Only an incomplete *final* frame (a torn write) may be truncated.
//   - A complete frame with a bad checksum, a sequence gap or an unknown
//     version is corruption. Halt and refuse readiness; never skip it.
//
// Failure model: process crash on a persistent local volume. Power loss and
// machine loss are out of scope for Tier 1 and documented as limitations.
package journal
