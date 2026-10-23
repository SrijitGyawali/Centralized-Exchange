# ADR-0007: Authenticated HTTP API and request recovery

- **Status:** Accepted
- **Date:** 2026-10-23

## Context

Week 4 made command results durable. Week 5 lets concurrent HTTP clients use
that engine without introducing a second owner of trading state. A dropped
response must never encourage a second trade, and account data must remain
private even when callers guess an order ID or request key.

## Decision

Use Go's standard HTTP server and method-aware ServeMux. Pre-provision bearer
tokens in `CEX_API_TOKENS`; each token identifies a nonzero user and an admin
role. Tokens for one user must have the same role and share a rate bucket.
There is no default credential, password database, JWT issuer or refresh token.
`GET /v1/session` is token login/verification. An empty token configuration
leaves health available and denies every protected request. Token values never
appear in startup logs or configuration errors. Deploy behind TLS outside
loopback; bearer tokens are credentials, and this server does not terminate TLS.

Authentication precedes the per-user token bucket, which precedes parsing.
Buckets are bounded by the configured users; arbitrary unauthenticated input
does not allocate new limiter state. `Retry-After` is seconds. Credential
rotation and limiter state take effect on restart; trading idempotency survives
restart through the journal.

### Wire contract

All mutation requests carry `Idempotency-Key`: 1–64 printable ASCII bytes,
scoped to the authenticated user across all command kinds. Prefer URL-safe keys
so result lookup needs no escaping. Repeat the same parsed body with the same
key to retrieve the original result; changing the body gives 409. Equivalent
decimal strings, such as `0.1` and `0.1000`, represent the same command.

JSON bodies must be one object of the documented string fields. Unknown,
duplicate, missing and null fields, numeric amounts, trailing JSON and bodies
over 16 KiB are rejected. Body endpoints require `application/json`. Amounts,
prices, quantities, IDs and sequence numbers are strings; decimal parsing never
passes through floating point. BTC precision is four digits, USDT funding
precision six digits, and prices are whole USDT ticks. Funding is bounded by
asset supply; individual order quantities remain capped at 10 BTC.

| Endpoint | Request / response |
|---|---|
| `GET /healthz` | Public process liveness |
| `GET /readyz` | Public engine readiness; halted storage returns 503 |
| `GET /v1/session` | `{user_id, admin}` for the supplied bearer token |
| `POST /v1/orders` | String fields: `symbol`, `type`, `side`, `time_in_force`, `price`, `quantity` |
| `DELETE /v1/orders/{id}` | No body; sequenced owner-only cancel with a new request key |
| `GET /v1/orders/{id}` | Owner's resting order and remaining `quantity` |
| `GET /v1/requests/{key}` | Original result for this user; lookup does not consume a sequence |
| `GET /v1/balances` | `seq` plus BTC/USDT rows with `asset`, `available`, `reserved` |
| `GET /v1/book?symbol=BTC-USDT` | Snapshot `seq`, `symbol`, `bids`, `asks`; symbol may be omitted |
| `POST /v1/admin/fund` | Admin-only string fields: `user_id`, `asset` (BTC/USDT), `amount` |

Orders support `symbol: BTC-USDT`, `type: limit`, `side: buy|sell`,
`time_in_force: GTC|IOC`. The result fields are `seq`, `accepted`, `rejected`,
`detail`, `duplicate`, `order_id`, `outcome`, `fills`, `remaining`, `canceled`.
Fills expose price and quantity only, never counterparty account/order IDs.
`seq` on a duplicate is the original deciding sequence, not the retry's WAL
sequence. Snapshot and balance sequences identify the state being read.

The current state retains resting orders, not a historical order-status index.
GET status returns 404 for filled, canceled, nonexistent and foreign orders.
The historical request result remains recoverable, but its original outcome
is not the order's current state. Cancel ownership is enforced inside Apply,
so a pre-handler ownership check cannot race a match or leak existence.

### Status and failure contract

| HTTP | Meaning |
|---|---|
| 200 | Accepted durable command, authenticated session, or successful read |
| 400 | Invalid input before admission; key is not consumed |
| 401 / 403 | Missing or invalid token / funding without admin role |
| 404 | Unknown/foreign/nonresting order, unknown private request key |
| 409 | Durable idempotency-key conflict |
| 413 / 415 | Body too large / unsupported content type |
| 422 | Durable business rejection, including insufficient funds or supply cap |
| 429 | Rate limit; not admitted |
| 503 | Overload, closed/halted engine, or uncertain storage outcome |
| 504 | Request deadline/cancellation ended the wait |

This supersedes the architecture sketch's original suggestion that every
business rejection use 200. Durable rejections still have the complete typed
result, and result lookup/retry maps them to the same status. Transport errors
use an `error` envelope. An admitted failure explicitly includes
`outcome_unknown: true`; clients must query or retry the same key. A timeout is
not a rollback. A 404 lookup while an admitted command is pending is not proof
it will never execute. A storage failure cannot produce an accepted result.

### Example (Bash, local paper trading)

```bash
export CEX_API_TOKENS='[{"token":"local-example-admin-change-me","user_id":"1","admin":true}]'
go run ./cmd/cexd
# From another shell, set TOKEN to the same local demonstration value:
TOKEN=local-example-admin-change-me
curl -H "Authorization: Bearer $TOKEN" localhost:8080/v1/session
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: initial-funds' localhost:8080/v1/admin/fund \
  -d '{"user_id":"1","asset":"USDT","amount":"10000"}'
curl -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: buy-1' localhost:8080/v1/orders \
  -d '{"symbol":"BTC-USDT","type":"limit","side":"buy","time_in_force":"GTC","price":"60000","quantity":"0.1"}'
curl -H "Authorization: Bearer $TOKEN" localhost:8080/v1/requests/buy-1
curl -H "Authorization: Bearer $TOKEN" localhost:8080/v1/balances
curl -X DELETE -H "Authorization: Bearer $TOKEN" \
  -H 'Idempotency-Key: cancel-1' localhost:8080/v1/orders/1
```

## Alternatives and consequences

JWT/session issuance would add an identity subsystem beyond this simulator's
scope. A mutex around state would undermine the single-writer design. Handler
retry loops could silently issue a different command; clients instead own their
stable request keys. Direct serialization of internal structs would leak
counterparties and integer unit details, so HTTP has explicit DTOs.

Private reads run through the engine's ordered queue; book reads use immutable
snapshots. Request timeouts bound waiting, server timeouts bound network I/O,
and write timeout must exceed request timeout. No trading path runs Apply in an
HTTP goroutine. The test suites and `go run ./tests/week5` exercise this contract.
