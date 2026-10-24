# 2026-10-23 — Week 5: HTTP API and group commit

## Starting point

Local main and GitHub both ended at `2d01cc5`, the week-4 track note. There
were no uncommitted files or remote week-5 commits to recover from the interrupted
cloud session. This session implemented the supplied week-5 design on
`codex/week-5-http-group-commit`.

## What changed and why

### Exact input and recovery reads

`domain.ParseAmount` parses funding by asset: four BTC decimals and six USDT
decimals, using integer arithmetic. Funding can exceed the per-order quantity
limit, but cannot exceed the asset supply cap. Syntax, precision, zero and
overflow cases are tested. Existing order price/quantity rules are unchanged.

`State.Lookup(user, key)` retrieves the original accepted or rejected command
result without consuming a sequence. It returns cloned fills and cannot reveal
another user's result. The engine exposes it through its read-only StateReader
interface, keeping HTTP private reads on the state-owner goroutine.

### Durable batches without a batching timer

`Journal.AppendBatch` validates and encodes every ordinary WAL frame before
writing. The production path performs one Write and one Sync for the buffer.
Invalid batches write nothing; write, short-write and sync failures poison the
journal until restart. Existing per-frame crash hooks and the WAL format remain
compatible. A batch is not an atomic transaction: recovery may find its complete
prefix and repair a torn final frame.

The engine drains only commands already available in its queue, at most 64 by
default. A read splits batches and retains its place in the queue. A lone
command proceeds immediately. The entire batch is durable before any command
is applied; each snapshot is published before its response. A failed batch
applies nothing and never sends success. Closing still drains admitted work.
Append-only alternative writers retain their original compatibility path.

### HTTP transport and configuration

The authenticated API provides token verification, place, cancel, resting-order
status, private request lookup, balances, book and admin funding. Bearer tokens
are provisioned through strict JSON environment configuration, with no default
secret. A token determines the user; clients cannot select an order owner.
Aliases share the same user bucket and must have the same admin role.

Auth precedes rate limiting and strict JSON parsing. Parsing rejects unknown or
duplicate fields, null/nonstring fields, trailing data, invalid UTF-8, oversized
bodies, invalid decimals and unsupported order parameters. Cancel accepts no
body. Prices, amounts, identifiers and sequence numbers are JSON strings.
Fills expose price/quantity without counterparty identities. Unknown and foreign
orders both return 404; cancel ownership stays inside the sequenced state change.

Mutations require `Idempotency-Key`. Accepted results return 200, remembered
business rejections use 422/404/409, and transport failures remain distinct.
After admission, timeout or storage failure explicitly reports an unknown
outcome. Retry the same key or query its original result. A retry consumes a new
WAL sequence but returns the original decision and does not move money again.

The real server now wires the recovered node into this API and applies configured
batch size, rate/burst limits and request/network timeouts. Token configuration
errors and startup logs do not disclose credentials. Public health checks remain
usable without a token. See [ADR-0007](../../docs/adr/0007-authenticated-http-api.md)
for endpoints/examples and [ADR-0008](../../docs/adr/0008-opportunistic-group-commit.md)
for durability semantics.

### End-to-end gate and benchmarks

`go run ./tests/week5` builds a real cexd executable, creates a temporary WAL,
authenticates, funds two accounts, places an ask, matches part of it with an IOC
buy, checks exact balances and book depth, tests ownership, and cancels the
remainder. It kills the server after acknowledged commands, starts it on the
same WAL, retries the original requests, and verifies identical outcomes,
unchanged balances and an empty book. Sequence checks are separate because
idempotent retries still consume journal sequences. The gate clears inherited
`CEX_*` settings and is included in GitHub CI.

Journal and real-node benchmarks compare BatchSize 1 and 64. Three recorded
runs gave median journal costs of 1,217,215 versus 20,734 ns/command; the
64-caller real-node workload gave 1,249,980 versus 32,621 ns/command. These are
local amortized costs, not tail-latency or production-capacity claims. Early
small eight-caller runs were noisy; the full commands, raw results and limits
are in [the evidence file](../../evidence/bench/2026-10-23-group-commit.txt).

## Verification and review

- `gofmt -l .` and `git diff --check`: clean.
- `go vet ./...`: passed.
- `go test -count=1 ./...`: passed, including existing matching, accounting,
  recovery and crash tests.
- `go test -race -count=1 ./...`: passed with the Windows GCC toolchain.
- `go run ./tests/week5`: passed against real HTTP and a restarted process.
- Three repetitions of each journal/node batch benchmark: passed and recorded.
- Independent whole-change review: no material defects found.

Targeted tests cover one write/one sync, read barriers, closed-queue draining,
no pre-sync effects/replies, failure halting, cloned lookup results, auth,
ownership, validation, overload, timeout semantics and credential redaction.
The API failure test proves a storage error returns 503 with unknown outcome,
never accepted success; the engine failure test proves no batch member applied.

Several initial shell invocations were slow. Editing imports while a delayed
build was in flight caused transient compiler import-map failures; verification
was rerun against stable source. The sandbox denied loopback sockets to the
standalone process gate, so its successful run used the host execution context.
These environment failures were not reported as application passes.

## Delivery structure

The work is organized into 30 substantive commits, with detailed behavior and
validation messages. Most commits touch one file; four pair implementation and
tests for exact parsing, lookup, configuration and server startup. The sequence
covers the plan, foundation, journal, engine, API components/tests, process gate,
CI, benchmarks/evidence, ADRs, configuration example, architecture, README and
this track note. No empty commits or artificial whitespace changes are needed.

## Current limits and next session

Order status currently describes resting orders. Filled/canceled orders return
404; original command outcomes remain available by request key. Tokens are
operator-provisioned, not issued by a password-login service. TLS termination,
credential management, WAL rotation and dedupe retention are outside this step.
The durability model remains process crashes on persistent storage, not disk
loss or machine loss. Week 6 can build market-data streaming on these durable
commands and immutable snapshots.
