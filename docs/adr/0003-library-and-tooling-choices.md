# ADR-0003: Library and tooling choices (third-party modules vs. standard library / from scratch)

- **Status:** Accepted
- **Date:** 2026-10-04

This record compares every library and tool considered so far against the
default approach chosen for this project, and explains *when* each choice
should be revisited. It answers one recurring question:

> **Should we pull in a third-party module, or use Go's standard library /
> write it ourselves?**

---

## 1. Summary table

| # | Area | Third-party option considered | Our default | Verdict | Revisit when |
|---|---|---|---|---|---|
| 1 | HTTP server / routing | **Echo** | `net/http` + Go 1.22 `ServeMux` | Stdlib now | Middleware duplication hurts (~week 5) |
| 2 | PostgreSQL driver | **pgx** (vs `database/sql`, `lib/pq`, GORM) | pgx | ✅ **Use pgx** (week 6) | n/a |
| 3 | DB migrations | **tern** (vs goose, golang-migrate) | tern | ✅ **Use tern** (week 6) | n/a |
| 4 | Logging | **zerolog** | `log/slog` | Stdlib | Profiling shows logging costs CPU |
| 5 | Validation | **go-playground/validator** | Hand-written `domain` parsers | From scratch | Tier 2 admin forms with many plain fields |
| 6 | Configuration | **koanf** (vs Viper) | Plain env vars in `internal/config` | From scratch now | >~10 settings or config files needed (~week 5–6) |
| 7 | Test assertions | **testify** | `testing` + **go-cmp** | Stdlib + go-cmp | Never for mocks; `require` only if desired |
| 8 | Test dependencies | **testcontainers-go** vs mocks | Real Postgres via testcontainers; hand fakes only for failure injection | ✅ **Use testcontainers** (week 6) | n/a |
| 9 | Task runner | **Taskfile** (go-task) | `Makefile` | Makefile | Makefile gets painful (~week 6–7) |
| 10 | Authentication | **Clerk** | Seeded tokens (Tier 1) behind an `Authenticator` interface | Seeded tokens now | Frontend login (week 6) / Tier 2 hardening |

**Final dependency count for Tier 1: ~3 modules.** They are pgx, tern and
go-cmp, plus testcontainers and a WebSocket library added in week 6.

---

## 2. The general trade-off: modules vs. from scratch

| | Using a third-party module | Standard library / from scratch |
|---|---|---|
| **Speed to first result** | ✅ Faster: features ready-made | Slower: you write the code |
| **Learning value** | Lower: the library hides the mechanism | ✅ Higher: you learn how Go really works |
| **Control** | You follow the library's design and defaults | ✅ Exactly the behaviour you need |
| **Correctness for edge cases** | ✅ Battle-tested by thousands of users | Only as good as your tests |
| **Maintenance** | Version upgrades, breaking changes, abandonment risk | ✅ The Go team maintains the stdlib; your own code changes only when you change it |
| **Security / supply chain** | Every module (and its dependencies) is attack surface; needs `govulncheck` | ✅ Smaller surface |
| **Build reproducibility** | Must pin versions in `go.sum` | ✅ Nothing to pin |
| **Coupling** | Library types can spread through the code (`echo.Context`, `zerolog.Event`) | ✅ Standard interfaces (`http.Handler`, `*slog.Logger`) |
| **Interview story** | "I know library X" | "I know what library X does underneath, and chose deliberately" |

### The rule this project follows

1. **Use the standard library when it is good enough.** Go's stdlib is
   unusually complete: HTTP, JSON, logging, testing, fuzzing, crypto.
2. **Use a module when building it yourself would be dangerous or a large
   distraction**, for example a PostgreSQL wire-protocol driver (pgx), a
   real database in tests (testcontainers), or secure login (Clerk, later).
3. **Never use a module that hides the parts this project exists to
   teach or prove**: transactions, validation of money, concurrency,
   durability.
4. **Keep every module at the edge.** Only shell packages (`api`,
   `projector`, `cmd/*`) may import third-party code. The pure core
   (`domain`, `command`, `book`, `state`) stays standard-library only.
5. **Every new dependency needs a reason written down** (this ADR or a new
   one), and is checked with `govulncheck` in CI.

---

## 3. Detailed comparisons

### 3.1 HTTP: Echo vs `net/http`

| Concern | `net/http` (Go 1.22+) | Echo |
|---|---|---|
| Routing `GET /v1/orders/{id}` | ✅ Built in | ✅ Built in + route groups |
| Middleware (log, recover, auth, CORS, rate limit) | Write yourself (~10–20 lines each) | ✅ Ready-made |
| JSON decoding | `json.Decoder` + `DisallowUnknownFields()`, strict | `c.Bind()` is lenient about unknown fields, so we would bypass it for orders anyway |
| Handler type | `http.Handler`, the universal interface | `echo.Context`, tied to Echo |
| WebSocket | Needs a library | Needs a library |
| Performance | More than enough | Slightly faster routing; irrelevant next to WAL `fsync` (ms) |
| Dependencies | 0 | 1 + transitive |

**Why stdlib:** routing takes microseconds, while each order waits
milliseconds for `fsync`, so the router can never be the bottleneck. Order
input needs strict decoding that Echo's binder doesn't give by default.
Tier 1 has only ~8 endpoints. Writing middleware teaches closures, handler
chaining and `context`.

**Switch if:** middleware or error-handling duplication becomes painful, or
you want Echo on your resume. Keep `echo.Context` inside `internal/api`.

```go
// stdlib (current)
mux.HandleFunc("GET /v1/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
    id := r.PathValue("id")
})
// Echo
e.GET("/v1/orders/:id", func(c echo.Context) error {
    id := c.Param("id")
    return c.JSON(http.StatusOK, resp)
})
```

### 3.2 PostgreSQL driver: pgx

PostgreSQL is only the **read model** (projection). It is never used for
matching, balances or dedupe, and it can be dropped and rebuilt from the
WAL.

| Option | Verdict |
|---|---|
| **pgx v5** (`jackc/pgx/v5`, `pgxpool`) | ✅ Native protocol, connection pool, batching, `COPY`, `BIGINT` ↔ `int64` |
| `database/sql` + pgx stdlib adapter | OK, but loses pgx-specific features |
| `lib/pq` | ❌ Maintenance mode |
| GORM (ORM) | ❌ Hides transactions and SQL, which is exactly what the projector must control |
| sqlc (on top of pgx) | 👍 Optional later: plain SQL → generated type-safe Go |

**From scratch?** No. Writing a Postgres wire-protocol driver is a huge
project and not what we're here to learn. This is a clear "use a module"
case.

**Why pgx:** the projector's key rule is *one transaction* that inserts
events, updates projections and advances the checkpoint. pgx makes
`Begin/Commit` explicit, and `COPY`/batches make a full rebuild fast. Only
`internal/projector` imports it.

### 3.3 Migrations: tern

| | **tern** | goose | golang-migrate |
|---|---|---|---|
| Databases | PostgreSQL only | Many | Many |
| Built on | **pgx** | `database/sql` | Own drivers |
| Format | One SQL file, `---- create above / drop below ----` | SQL or Go | Separate `.up.sql` / `.down.sql` |
| Embeddable in binary | ✅ | ✅ | ✅ |

**From scratch?** A tiny migrator (apply numbered SQL files, record a
version) is about 100 lines and a nice exercise. tern does it correctly,
including locking, with the same author and ecosystem as pgx.

**Rules:** never edit an applied migration (add a new one); money columns
are `bigint`; the DB URL comes from env (`CEX_DATABASE_URL`). Because the
projection is rebuildable, a bad migration in dev means drop, migrate and
rebuild.

### 3.4 Logging: zerolog vs `log/slog`

| | `log/slog` (current) | zerolog |
|---|---|---|
| Structured JSON | ✅ | ✅ |
| Speed | Fast | Faster (zero-alloc) |
| Dependencies | 0 | 1 |
| API | `logger.Info("msg", "k", v)` | `log.Info().Str("k", v).Msg("msg")` |
| Swappable backend | ✅ Handler interface (can even use zerolog underneath) | Tied to zerolog |

**Why slog:** the matching core never logs, because `Apply` is pure and
logging is I/O. Logs happen only at the edges (startup, errors, slow
requests). One slog call (~µs) is invisible next to `fsync` (~ms).

**Switch if:** a week-7 profile shows logging cost. Then plug zerolog in
*as a slog handler* (one line in `main.go`), measure before and after, and
save the results in `evidence/`.

**Rules regardless of library:** no logging inside `Apply`; never log
secrets or tokens; include `seq` / `order_id` in every relevant line.

### 3.5 Validation: go-playground/validator vs domain parsers

| Rule we need (ADR-0002) | Struct tag can do it? |
|---|---|
| Field present, `side ∈ {buy, sell}` | ✅ |
| `"0.1"` has ≤ 4 decimals (lot = 0.0001 BTC) | ❌ |
| Reject `"1e5"`, `"+5"`, `" 1"` | ❌ |
| String → exact integer lots/ticks **without float64** | ❌ (validators check, they don't convert) |
| `price × qty × 100` doesn't overflow `int64` | ❌ |
| Symbol exists in the versioned config | ❌ |
| User has enough balance | ❌ (only the engine knows, in sequence order) |

**Why from scratch:** the rules that matter for money can't be expressed as
tags. We follow **"parse, don't validate"**: `domain.ParseQty("0.1")`
*returns* a `Qty`. Holding a `Qty` proves it is valid, and the type carries
that guarantee everywhere afterwards. Parsers are plain functions, ideal for
table-driven tests and **fuzzing**.

Three validation layers:

```
api        JSON shape: body limit, unknown fields, required fields     → 400
domain     exact decimal parsing, precision, range, overflow           → 400
state      balance, self-trade, idempotency conflict                   → typed, durable rejection
```

**Switch if:** Tier 2 admin endpoints with many plain fields (emails,
names). Never for money.

### 3.6 Configuration: koanf vs plain env

| | Plain env (`internal/config`, current) | koanf | Viper |
|---|---|---|---|
| Env vars | ✅ | ✅ | ✅ |
| Files (YAML/TOML) + layering | ❌ | ✅ | ✅ |
| Nested keys, hot reload | ❌ | ✅ | ✅ |
| Validation | ✅ Our own, all errors at once | ❌ Still yours | ❌ Still yours |
| Weight | 0 deps, ~60 lines | Light, modular | Heavy, lowercases keys |

**Why from scratch now:** 4 settings don't need a framework. `Load(getenv)`
is trivially testable.

**Switch to koanf (not Viper) when:** there are more than ~10 settings,
natural groups (`http.*`, `wal.batch_max`, `postgres.*`), or a deployed
config file plus env overrides for secrets. Only the inside of `Load()`
changes.

**Critical rule:** **trading rules are not config.** Lot size, tick size,
bounds, fees and the symbol list are **versioned engine state, changed only
by sequenced WAL commands**. If they came from an editable file, replaying
old commands would produce different results, which breaks determinism and
recovery.

### 3.7 Testing: testify vs `testing` + go-cmp

| | `testing` + go-cmp (chosen) | testify |
|---|---|---|
| Dependencies | go-cmp only (small, Google) | 1 module |
| Verbosity | A few more lines | Shorter |
| Failure messages | Written by you, specific | Generic `expected/actual` |
| Large state comparison | ✅ `cmp.Diff` shows the exact field path | Less precise |
| Mocks | Hand-written fakes, compiler-checked | `testify/mock`, string-based, breaks at runtime |
| Fuzzing, benchmarks, table tests | ✅ Native | Works |

**Why:** the important tests compare **whole states** (engine vs reference
model, recovered vs replayed). `cmp.Diff` pinpoints the first wrong field:

```go
if diff := cmp.Diff(want, got); diff != "" {
    t.Fatalf("state mismatch at seq %d (-want +got):\n%s", seq, diff)
}
```

**If you use testify anyway:** `require` only. Never `mock` or `suite`.

### 3.8 Test dependencies: testcontainers vs mocks

| Layer | Tool | Why |
|---|---|---|
| `domain`, `book`, `state` | Nothing; pure values | No I/O to fake |
| `engine` | **Hand fake** `Journal` | Must make `Sync` fail at exactly seq N; a real disk can't do that on demand |
| `journal` | **Real temp files** (`t.TempDir()`) | Files are cheap and real |
| `projector`, DB-backed `api` | **testcontainers (real Postgres)** | A mock would never catch bad SQL, unique-key violations, rollback bugs or failed migrations |
| Whole system | **Subprocess crash harness** | Needs a real process kill |

**Rule of thumb:** use the real thing when it is cheap and deterministic.
Use a fake only to **inject failures** the real thing can't produce.

Practical setup: `//go:build integration` tag, a `make test-integration`
target, one container per package in `TestMain`, pinned image
(`postgres:17-alpine`), Docker Desktop running on Windows, and a separate
CI job. Use docker compose for local dev and demos, and testcontainers for
automated tests.

### 3.9 Task runner: Taskfile vs Makefile

| | Makefile (current) | Taskfile (`go-task`) |
|---|---|---|
| Install | `make` (already present) | Separate `task` binary |
| Syntax | Quirky: tabs, `$$` | Clean YAML |
| Windows | Needs Git Bash/MSYS (works here) | Native |
| Features | Basic | `--list`, skips unchanged work, `.env`, includes |

**Why Makefile:** it works today (`make check`, `make build`), it's universal,
and each target is one readable `go` command. Two runners would duplicate
work. **Switch if** targets for Docker, migrations, integration tests and
load tests make it clumsy (~week 6–7). Each target is a one-line `go`
command, so migrating takes minutes.

### 3.10 Authentication: Clerk vs seeded tokens / own API keys

| | Question | Who handles it |
|---|---|---|
| **Authentication** | Who are you? | Clerk (later) or seeded tokens (Tier 1) |
| **Authorization** | May you do this? (cancel *your* order, see *your* balance, operator-only halt) | **Always our code**, in `api` + `state` |

| ✅ Clerk pros | ❌ Clerk cons |
|---|---|
| Secure login, MFA, email verification without building it | External vendor, lock-in, paid beyond the free tier |
| Polished UI for the frontend | Built for **humans in browsers**, not trading bots |
| JWTs verified **locally** via cached JWKS (no network call per order) | The demo needs internet and keys; a fresh-machine quickstart gets harder |
| | Zero help with authorization |

**Exchange-specific gap:** trading bots use **API keys + HMAC request
signatures** (key ID + signature over method/path/body/timestamp, with a
replay window). Clerk doesn't provide this, so we build it ourselves in
Tier 2.

**Plan by stage:**

| Stage | Approach |
|---|---|
| Tier 1 | Seeded tokens per test user: no dependencies, works offline |
| Week 6 frontend | Optional Clerk for browser login |
| Tier 2 (week 14) | Clerk (or similar) for humans **+** our API keys/HMAC for bots, revocation, operator role, cross-user denial tests |

**Design now so it's swappable:** an `Authenticator` interface in `api`
returns a `UserID`. Clerk IDs never enter the core; mapping a Clerk user to
our `UserID` is a sequenced "register user" command in the WAL.

---

## 4. Consequences

- **Easier:** few dependencies to pin and audit; a pure core with only
  stdlib imports; deep understanding of `net/http`, `slog`, `testing` and
  validation; every library choice is defensible in an interview.
- **Harder:** more hand-written code (middleware, parsers, fakes), and that
  code needs its own tests.
- **Evidence this ADR holds:**
  - `go list -deps ./internal/domain ./internal/book ./internal/state ./internal/command`
    shows only standard-library packages.
  - `go.mod` `require` lines match the table in §1.
  - `govulncheck ./...` runs in CI once the first dependency is added.
- **Revisit:** each row in §1 has a trigger. When a trigger fires, write a
  new ADR that supersedes the relevant section. Don't edit this one.
