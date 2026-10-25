# ADR-0010: Public market-data feed and the minimal web UI

- **Status:** Accepted
- **Date:** 2026-10-25

## Context

Week 6 asks for a live view of the market and a minimal page: book, trade
tape, order form, and the sequence numbers that show freshness. The
non-negotiable requirement (the week-6 gate) is **"a slow browser never
blocks matching."** One stalled phone on a bad network must not be able to
slow down every trade.

## Options considered

1. **The engine pushes to viewers.** The engine's loop would wait on
   network writes, which is exactly the failure to avoid. ❌
2. **One shared Go channel read by all viewers.** A channel with many
   receivers *distributes* messages (each goes to one receiver); it doesn't
   broadcast. And one slow reader stalls the sender. ❌
3. **A hub that reads the lock-free snapshot, with one bounded buffer per
   viewer** ✅. The engine never knows the viewers exist.
4. **Incremental updates** vs **full snapshots** ✅ (Tier 1). Incremental
   needs per-symbol sequences, buffering and gap detection on the client.
   Full snapshots are robust (any message redraws everything) and small
   enough here: 20 levels per side plus 50 trades.

## Decision

### Feed (`internal/marketdata`, `GET /stream/book`)
- The hub polls `engine.Snapshot()` (an atomic pointer to an immutable
  value) every **100 ms**, and broadcasts only if the Seq changed. That
  gives a bounded rate, and an idle market is silent.
- Each message is encoded **once** (JSON, all numbers as exact decimal
  strings) and the same bytes go to every subscriber.
- **One bounded channel (8) per subscriber**, sent non-blocking. A full
  channel means the subscriber is too slow: it's disconnected with close
  code **1013** and the reason `slow consumer: reconnect to get a fresh
  snapshot`. Each connection's writer also has a 5 s write timeout.
- New subscribers get the latest snapshot immediately.
- Limits: 1,000 subscribers. The connection uses the library's default
  same-origin check.
- The engine snapshot gained a **public trade tape**: the last 50 fills
  with price, quantity, aggressor side and Seq, **never users or order
  IDs**. It's cloned into each snapshot, so published values are never
  shared with the engine's growing slice. Duplicates add nothing. The tape
  starts empty after a restart; the read model holds the full history.
- The stream is **public and unauthenticated**, because browsers can't set
  an Authorization header on a WebSocket and the data is public anyway.
  Private order updates (Tier 2) will need authentication.

### Shutdown
`http.Server.Shutdown` doesn't track hijacked (WebSocket) connections, so
the hub is stopped through `srv.RegisterOnShutdown`. Every stream gets
"server shutting down" (1013) instead of hanging.

### Library
`github.com/coder/websocket`: maintained, small, context-aware. It's
used only in `internal/marketdata` and tests (ADR-0003's planned WebSocket
dependency).

### Web UI (`internal/webui`, `GET /` and `/ui/…`)
- Plain HTML, CSS and JavaScript, embedded with `go:embed`: no build
  step, no framework.
- It shows the book (best ask just above the spread), the trade tape,
  `seq`, and with a token: `applied_seq`, `projected_seq`, the projection
  lag, balances, an order form, and a "retry with the same
  Idempotency-Key" button that demonstrates deduplication.
- Security:
  - text is written only with `textContent` (tested: no `.innerHTML` and
    similar)
  - no inline script, enforced by a strict Content-Security-Policy
    (tested)
  - `nosniff`, `no-referrer`, `frame-ancestors 'none'`
  - the token is kept in `sessionStorage` only
- The spread is computed with `BigInt`, never floating point.

## Consequences

- **Easier:** any number of viewers at no cost to matching. Clients are
  trivial (a reconnect fixes everything), and the demo is visible in a
  browser.
- **Harder:** bandwidth is higher than with incremental updates; up to
  100 ms of display staleness is accepted; the trade tape isn't persisted
  across restarts.
- **Evidence:** hub unit tests (initial snapshot, publish-on-change, slow
  subscriber dropped while others receive everything and publishing never
  waits, limits, shutdown); WebSocket tests including the gate (a browser
  that stops reading is cut off; the slowest order was 2.5–6 ms across 5
  runs; the healthy browser keeps receiving); the real server keeps a
  stream alive past its HTTP WriteTimeout and closes it with a reason on
  shutdown; UI handler tests for headers, CSP compatibility and HTML
  injection sinks.
