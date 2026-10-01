// Package api is the HTTP transport. It turns untrusted requests into
// validated commands and turns results into HTTP responses.
//
// # Responsibilities
//
//   - Day 1: liveness (/healthz) and readiness (/readyz) endpoints.
//   - Week 5: order placement, cancel, order status, request-result lookup,
//     balances and book; seeded-token authentication; ownership checks;
//     rate limits; body-size limits; rejection of unknown JSON fields.
//
// # Rules
//
//   - Validate everything before admission. Malformed input gets a 400 and
//     never reaches the journal.
//   - Money and large IDs are JSON strings, parsed with domain's exact
//     decimal parser.
//   - Status codes: 400 malformed, 401/403 auth, 409 idempotency conflict,
//     429 rate limited, 503 overloaded before admission. A business
//     rejection (e.g. insufficient funds) is a typed result, not an HTTP
//     error.
//   - A timeout means "outcome unknown". The client retries with the same
//     key or queries the request result.
//   - Handlers never read engine state directly. They submit commands or
//     read immutable snapshots.
package api
