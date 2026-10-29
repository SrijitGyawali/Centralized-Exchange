# track/

A learning journal: one note per working day, written as the system was
built. Each explains what was done, why, the Go ideas it used, and ends with
self-check questions. Read in order, they are the story of the design.

| Date | Week | Topic |
|---|---|---|
| [1 Oct](2026-10-01/README.md) | 0 | The repository skeleton, the health endpoint, CI |
| [4 Oct](2026-10-04/README.md) | 0 | Choosing tools and libraries (ADR-0003) |
| [16 Oct](2026-10-16/README.md) | 1 | Money as integers: `internal/domain` |
| [17 Oct](2026-10-17/README.md) | 1 | The order book: `internal/book` |
| [18 Oct](2026-10-18/README.md) | 2 | Matching: price-time priority, partial fills, IOC |
| [19 Oct](2026-10-19/README.md) | 3 | The money side: reservations, ledger, idempotency |
| [20 Oct](2026-10-20/README.md) | 3 | The engine: one goroutine owns the state |
| [21 Oct](2026-10-21/README.md) | 4 | Saving to disk: the write-ahead journal and recovery |
| [23 Oct](2026-10-23/README.md) | 5 | The HTTP API and group commit |
| [25 Oct](2026-10-25/README.md) | 6 | The live market-data feed and the web UI |
| [26 Oct](2026-10-26/README.md) | 6 | Reviewing week 5; the SQL read model |
| [27 Oct](2026-10-27/README.md) | 7 | Measuring the exchange: metrics, load tests, Compose |
| [28 Oct](2026-10-28/README.md) | 8 | Freeze and demonstrate: the inspector, drills, v0.1.0 |
