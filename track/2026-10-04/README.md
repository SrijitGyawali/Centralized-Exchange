# 4 October 2026: choosing tools and libraries

> **One-line summary:** no code today. We asked "should we use library X?"
> for ten tools and recorded every answer, with reasons and "switch when…"
> triggers, in [ADR-0003](../../docs/adr/0003-library-and-tooling-choices.md).

---

## 1. The question behind every question

Every tool question today was really one question:

> **Module, or standard library / from scratch?**

```
                 Is building it ourselves dangerous or a huge distraction?
                         │
             yes ────────┴──────── no
              │                      │
        USE A MODULE          Does the stdlib already do it well?
   (pgx, tern, testcontainers,       │
    Clerk later)            yes ─────┴───── no
                             │                │
                       USE STDLIB       Is it what this project exists
               (net/http, slog,         to teach or prove?
                testing)                      │
                                    yes ──────┴────── no
                                     │                  │
                              WRITE IT OURSELVES   USE A SMALL MODULE
                           (domain parsers, config,   (go-cmp)
                            WAL, middleware)
```

And one placement rule: **third-party code lives only in the shell**
(`api`, `projector`, `cmd`). The pure core (`domain`, `command`, `book`,
`state`) imports the standard library only.

---

## 2. Today's decisions at a glance

| Asked about | Decision | One-line reason |
|---|---|---|
| Echo | `net/http` for now | Router speed is irrelevant next to fsync; strict JSON needed anyway |
| pgx | ✅ Use (week 6) | Best Postgres driver; explicit transactions for the projector |
| tern | ✅ Use (week 6) | Postgres-only, built on pgx, plain SQL migrations |
| zerolog | `log/slog` | The core never logs; slog's speed is plenty |
| validator | Own `domain` parsers | Tags can't check decimals, ticks or overflow; parse, don't validate |
| koanf | Plain env now, koanf later | 4 settings don't need a framework; trading rules are never config |
| testify | `testing` + go-cmp | `cmp.Diff` pinpoints state mismatches; no string-based mocks |
| testcontainers | ✅ Use for Postgres tests | Mocking SQL tests the mock, not Postgres |
| Taskfile | Keep the Makefile | It already works; revisit in week 6–7 |
| Clerk | Seeded tokens now; Clerk optional later | Clerk handles authentication only; authorization is always ours |

---

## 3. Words learned today

| Term | Meaning in our project |
|---|---|
| **Single-module repo** | One repository, one `go.mod`, one product. Not a monorepo (yet). |
| **Monorepo** | One repository with many separate projects. We'd become a small one if `web/` (frontend) is added. |
| **Standard Go layout** | `cmd/` for binaries, `internal/` for private code. Recommended on go.dev. |
| **Modular monolith** | One program, split into packages with strict boundaries. |
| **Functional core, imperative shell** | Pure logic inside (`state`, `book`, `domain`); I/O outside (`api`, `journal`, `projector`). |
| **Hexagonal / ports & adapters** | Port = an interface the core needs (`Journal`); adapter = the real implementation (file WAL, pgx). |
| **CQRS read model** | Writes go through the engine; history queries come from PostgreSQL. |
| **Command sourcing** | The WAL stores commands; state is rebuilt by replaying them. |
| **Parse, don't validate** | A parser *returns* a typed value (`Qty`), so holding one proves it's valid. |
| **Authentication vs authorization** | "Who are you?" vs "May you do this?" |
| **HMAC API keys** | How trading bots authenticate: a signature over each request. |
| **Fake vs mock vs real** | Fake = a small hand-written implementation; mock = string-configured stub (avoid); real = temp files or a container. |

---

## 4. Check yourself

1. Why doesn't Echo's speed matter for our exchange?
2. Why must lot size and tick size never come from a koanf config file?
3. Name two bugs a mocked database would hide that testcontainers would
   catch.
4. What does Clerk *not* do that our code must always do?
5. Why does `ParseQty` return a `Qty` instead of a `bool`?
6. Which packages are allowed to import third-party modules?

---

## 5. Next session

Week 1, session 2: implement `internal/domain` (units, exact decimal
parsing, checked notional) test-first, using only the standard library.
That is decision #5 from today turned into code.
