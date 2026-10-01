# tests/

Cross-package and black-box tests. Unit tests do **not** live here. They
sit beside the code they test (`internal/book/book_test.go`).

Planned contents:

| Directory | Week | What it proves |
|---|---|---|
| `crash/` | 4 | A subprocess harness kills `cexd` at injected points (before append, partial frame, after write before Sync, after Sync before Apply, after Apply before reply) and checks that every acknowledged effect survives exactly once. |
| `e2e/` | 5 | Place → match → query → cancel over real HTTP, including a lost response retried with the same key. |
| `recovery/` | 4-6 | Full-state digest after restart equals an independent replay of the recovered log, and the SQL rebuild agrees. |

Integration tests that need Docker or PostgreSQL will use a build tag
(`//go:build integration`) so `go test ./...` stays fast and hermetic.
