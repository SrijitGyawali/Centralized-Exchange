# Architecture Decision Records

One decision per file: the context, the options considered, what was
chosen and why, and the consequences. New decisions start from
[`0000-template.md`](0000-template.md).

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-scope-and-single-writer.md) | One symbol, one process, **one goroutine owns all trading state** | Accepted |
| [0002](0002-units-and-fixed-point.md) | Integer ticks, lots and atoms with checked arithmetic; no floats | Accepted |
| [0003](0003-library-and-tooling-choices.md) | Standard library first; each third-party module justified (pgx, tern, testcontainers, coder/websocket, Prometheus) | Accepted |
| [0004](0004-accounting-reservations-ledger-idempotency.md) | Reservations, a double-entry ledger per asset, idempotency keys with stored results | Accepted |
| [0005](0005-acknowledgement-and-request-lifecycle.md) | Acknowledge only after durable + applied; timeouts never roll back; `outcome_unknown` | Accepted |
| [0006](0006-journal-format-recovery-and-failure-model.md) | Checksummed journal frames; torn tail cut, corruption refused; the process-crash failure model | Accepted |
| [0007](0007-authenticated-http-api.md) | Bearer-token API, ownership privacy, strict JSON, status codes, request recovery | Accepted |
| [0008](0008-opportunistic-group-commit.md) | Group commit of already-queued commands, with no batching timer | Accepted |
| [0009](0009-sql-read-model-projection.md) | A PostgreSQL read model that follows the journal with transactional checkpoints | Accepted |
| [0010](0010-public-market-data-feed-and-web-ui.md) | A public WebSocket feed that drops slow viewers; an embedded web UI | Accepted |
| [0011](0011-metrics-and-private-admin-port.md) | Prometheus metrics behind one package, on a private admin port; a precise clock | Accepted |
| [0012](0012-open-model-load-testing.md) | Open-model load testing with the server's own attribution of time | Accepted |
