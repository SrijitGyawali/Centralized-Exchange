# tests/

Cross-package, black-box programs that run the **real binaries**. Unit tests
live beside the code they test (`internal/book/book_test.go`); integration
tests that need Docker use the `integration` build tag.

| Directory | Run with | What it proves |
|---|---|---|
| [`crash/`](crash/) | `go test ./tests/crash` | A subprocess harness kills the node at every journal boundary (before / mid / after write, after fsync, after apply) and checks that every acknowledged effect survives exactly once and the recovered digest matches an independent replay. |
| [`e2e/`](e2e/) | `go run ./tests/e2e` | The HTTP API end to end against a crash-restarted `cexd`: funding, matching, queries, ownership, cancellation, recovery and idempotent retries. |
| [`drills/`](drills/) | `make drills` | Hard kills under load with lookups and retries, money checks, hot and cold journal restores, and recovery time at 100k and 1M commands. Report: [`evidence/crash/`](../evidence/crash/). |
| [`demo/`](demo/) | `make demo` | The three-minute demonstration ([docs/DEMO.md](../docs/DEMO.md)); fails if a retry after a crash is not the original result. |

CI runs all four on every push (the drills in a quick, one-kill form).
