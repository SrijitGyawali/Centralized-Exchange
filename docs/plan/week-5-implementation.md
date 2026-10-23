# Week 5: HTTP API and group commit

Date: 2026-10-23. Starting point: `2d01cc5` (week 4); local and remote main are clean.
The user's supplied design and authorization cover implementation, detailed
file-focused commits (target 25–30), verification and pushing to GitHub.

## Design and constraints

Use Go 1.26.5 and the standard library. Preserve the single state owner,
existing WAL format and replay compatibility. All money and identifiers on
the HTTP wire are strings. Never acknowledge a command before durable storage
and application. Preserve per-user idempotency, ownership privacy, bounded
admission, crash hooks and graceful draining.

Collect only immediately available queued commands, at most BatchSize (default
64); no timer or delay to fill a batch. A queued read is a barrier. Validate and
encode all frames before one normal write and one sync; a storage error halts
the engine without applying that batch. Batches are a durability optimization,
not atomic transactions: recovery can find a complete prefix of an interrupted
batch. Queries remain ordered and snapshots publish before each reply.

Bearer tokens are pre-provisioned environment credentials mapped to a nonzero
user and optional admin role. No passwords, session issuer or default secret.
An authenticated GET /v1/session verifies the token. Health stays public; all
/v1 routes require authentication. Rate limits are per user (including aliases),
with bounded state derived from configured identities. Admin funding is the
only way HTTP creates simulated money.

Orders use symbol BTC-USDT, type limit, side buy/sell, time_in_force GTC/IOC,
price and quantity decimal strings. Mutations require Idempotency-Key (1–64
printable ASCII bytes). Strict JSON rejects unknown or duplicate fields,
wrong types, null, trailing values and oversized bodies. GET order status
describes resting orders; filled/canceled/nonexistent/foreign IDs all give 404.
Historical command outcomes are available through per-user request lookup.

Business failures have durable result bodies: 422 for invalid/funding failures,
404 for unknown or foreign orders, 409 for key conflicts; accepted operations
return 200. Transport errors are distinct: 400/401/403/413/415/429/503/504.
An admitted timeout or storage failure explicitly says outcome_unknown and
instructs clients to retry the same key or query the original result.

## Execution checklist

- [x] Foundation: test then implement domain.ParseAmount(asset, string), bounded
  by asset supply rather than order size; State.Lookup(user, key) returns clones;
  expose lookup through engine.StateReader. Test owner isolation and copying.
- [x] Storage: test then implement Journal.AppendBatch and engine batching;
  verify one sync, validation before writes, sticky write/sync failure, queued
  query barriers, draining, isolated request progress and replay compatibility.
  Files: internal/journal/{journal.go,batch_test.go,bench_test.go},
  internal/engine/{engine.go,batch_test.go}, internal/node/bench_test.go.
- [x] HTTP: test then build router, auth, limiter, JSON decoder, DTO/results,
  mutation handlers and query handlers. Cover every endpoint, ownership,
  validation/status mapping, idempotency and failed storage. Files are scoped
  to internal/api; retain NewRouter for existing health tests and add NewServer.
- [x] Configuration: test then load strict JSON token configuration, bounded
  rate/burst/batch settings and positive request/server timeouts. Wire recovered
  node, router and settings in cmd/cexd. No tokens means protected routes deny.
- [x] Gate: an executable Go end-to-end gate launches the real server process,
  funds, places, matches, queries, cancels, restarts the same WAL and retries
  without duplicates. Include storage-failure assertions in API/engine tests.
- [x] Documentation: ADR-0007 (HTTP), ADR-0008 (group commit), API examples,
  .env.example, architecture, README, benchmark evidence and dated track note.
- [x] Review: inspect all changes against requirements; resolve findings, run
  gofmt, go vet, go test, race tests, process gate and measured benchmarks.
- Delivery protocol: create 25–30 substantive file-focused commits with detailed
  purpose/behavior/validation messages; push and verify remote commit identity.

## Validation commands

Use `go test ./internal/domain ./internal/state` for the foundation;
`go test ./internal/journal ./internal/engine ./internal/node ./tests/crash`
for durability; `go test ./internal/api ./internal/config ./cmd/cexd` for HTTP.
The final gate is `go run ./tests/week5`, followed by `go vet ./...` and
`go test -race ./...`. Benchmark commands and measured results are recorded
in evidence/bench/2026-10-23-group-commit.txt. No throughput claim precedes a run.

## Progress

Implementation and independent review complete. Full normal/race tests, vet and the process gate passed. No uncommitted or remote week-5 implementation
was present at the start. Implementation and review evidence are recorded in the dated
track note for 2026-10-23. Delivery follows the commit and push protocol above.
