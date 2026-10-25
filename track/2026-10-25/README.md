# 25 October 2026: the live market-data feed and the web UI (week 6, part 2)

> **One-line summary:** the exchange can now be **watched in a browser**.
> A hub streams the order book and a public trade tape over WebSocket to any
> number of viewers, and a viewer that stops reading is **disconnected**
> instead of slowing matching down. A small embedded web page shows the book,
> the tape, the sequence numbers, balances and an order form.

Plan reference: week 6. Gate: **"a slow browser never blocks matching."**
Decisions in [ADR-0010](../../docs/adr/0010-public-market-data-feed-and-web-ui.md).
The SQL read model (part 1) is in [ADR-0009](../../docs/adr/0009-sql-read-model-projection.md).

---

## 1. The problem in one picture

Matching is one goroutine doing one thing at a time. If it ever waits on a
network write, every trader waits with it:

```
 BAD:  engine ──write──► slow phone on 3G (blocked for 4 s)
          └──► every other order queues behind that write
```

So the engine must **never know viewers exist**. It already publishes an
immutable `Snapshot` through an atomic pointer after every command (week 3).
Anyone can read it at any time without a lock and without asking the
engine goroutine.

---

## 2. The hub

```
 engine.Snapshot()  (atomic load, never touches the engine goroutine)
       │  every 100 ms, only if Seq changed
       ▼
 Hub ── encode JSON ONCE ──► viewer 1: chan (8) ──► writer goroutine ──► browser
                          ├─► viewer 2: chan (8) ──► …
                          └─► viewer N: chan FULL → disconnect (1013, "slow consumer")
```

| Rule | Why |
|---|---|
| Poll every 100 ms, publish only on a new Seq | Bounded rate whatever the trading load; an idle market sends nothing |
| Encode once, share the same `[]byte` | 1,000 viewers cost one `json.Marshal`, not 1,000 |
| One bounded channel **per viewer** | A single shared channel would hand each message to *one* reader (distribute), not all (broadcast) |
| Non-blocking send (`select { case ch <- m: default: drop }`) | The hub never waits for anybody |
| Full channel → disconnect with a reason | A viewer 8 messages behind is showing stale prices; reconnecting gives a fresh, correct snapshot |
| New viewer gets the latest snapshot immediately | No blank screen until the next trade |
| Every message is a **complete** snapshot | One message redraws everything; reconnect is the only recovery a client needs |

Incremental updates (send only what changed) are smaller, but need
per-symbol sequences and gap detection in the client. That's Tier 2.

---

## 3. The public trade tape

The snapshot gained `Trades`: the **last 50 fills**, each with Seq, price,
quantity and aggressor side, and **never** user IDs or order IDs.

- The engine owner appends fills after a successful `Apply`. Duplicates
  (idempotent retries) add nothing, because nothing new traded.
- `slices.Clone` copies the tape into each snapshot, so a published value is
  never shared with the slice the engine keeps growing. A test holds an old
  snapshot, trades more, and checks the old one didn't change.
- After a restart the tape starts empty. Full history lives in PostgreSQL.

---

## 4. Two real problems found today (and fixed)

| Found by | Problem | Fix |
|---|---|---|
| Reading the `net/http` docs, then a test | `Server.Shutdown` **doesn't track hijacked connections**, and a WebSocket is one. Streams would hang until the process exited | `srv.RegisterOnShutdown(stopHub)`: when shutdown starts, the hub closes every stream with 1013 "server shutting down" |
| The "no innerHTML" test | It failed on a **comment** that mentioned the word | Search for the actual sinks: `.innerHTML`, `.outerHTML`, `insertAdjacentHTML`, `document.write` |

A third risk was checked, not assumed: the server has a 2 s `WriteTimeout`
for normal requests. A test keeps a stream open 3 s past it and checks it's
still receiving.

---

## 5. The web page

Plain HTML, CSS and JavaScript in `internal/webui/static`, compiled into the
binary with `//go:embed`. There's no build step and no framework.

| It shows | From |
|---|---|
| Book (asks above, bids below, spread in the middle), trade tape, `seq`, connection status | `GET /stream/book` (public) |
| `applied_seq`, `projected_seq`, projection lag | `GET /v1/status` (token) |
| Balances, order form, "retry with the same Idempotency-Key" | `/v1/...` (token) |

**Security rules (all tested):**

- Text goes on the page **only** through `textContent`, never parsed as HTML.
  Even a malicious value could only ever appear as text.
- A strict **Content-Security-Policy**: scripts only from this server and **no
  inline script**, so injected `<script>` can't run.
- `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
  `frame-ancestors 'none'` (no clickjacking).
- The token is kept in `sessionStorage`: gone when the tab closes.
- The spread is computed with `BigInt`. Prices are exact decimal strings,
  and floating point would round them.

The retry button is the week-5 idempotency lesson made visible: press it and
the server returns the **same** order, not a second one.

---

## 6. Evidence

| Test | What it proves |
|---|---|
| `TestMessageFormat` | The exact JSON a client receives (numbers as strings) |
| Hub unit tests | Initial snapshot, publish only on change, subscriber limit, shutdown reason |
| Slow subscriber | It's dropped with "slow consumer" while a healthy one receives **every** message, and publishing never waits |
| **`TestSlowBrowserNeverBlocksMatching`** | A real WebSocket client that stops reading is cut off; meanwhile the slowest order took **2.5–6 ms** across 5 runs, and a healthy browser kept receiving |
| `TestStreamDeliversTheLiveBook` | Orders placed through the engine appear on the stream with the right Seq |
| `TestShutdownClosesStreamsWithAReason` | Close code 1013 and "server shutting down" |
| `TestSnapshotCarriesRecentPublicTrades` | Tape contents, duplicates add nothing, cap of 50, old snapshots immutable |
| UI handler tests | Security headers and CSP, no inline scripts, assets and 404, no HTML sinks in `app.js` |
| cexd end to end | The real server serves the page, keeps a stream past its WriteTimeout, closes it with a reason on Ctrl-C |

Everything else still passes: `go test -race ./...`, the week-5 process gate,
and the PostgreSQL integration tests.

---

## 7. Go ideas met today

| Idea | Where | In one line |
|---|---|---|
| `atomic.Pointer` snapshot | `engine` | Readers never lock and never wait for the writer |
| Fan-out | `hub.go` | One channel per receiver; Go channels distribute, they don't broadcast |
| `select` with `default` | `tick` | Try to send; if the buffer is full, do something else right now |
| `time.Ticker` | `Run` | A steady beat; `defer t.Stop()` releases it |
| `sync.Mutex` + map of subscribers | `Hub` | Short critical sections; never hold the lock while doing I/O |
| `slices.Clone` | `publish` | Copy before sharing, so the owner can keep appending |
| `context.WithTimeout` per write | `ws.go` | One stuck write can't hold a goroutine forever |
| `c.CloseRead(ctx)` | `ws.go` | Read (and discard) in the background so pings and close frames are handled |
| `srv.RegisterOnShutdown` | `cexd` | Hook for connections `Shutdown` can't see |
| `//go:embed static` + `fs.Sub` | `webui.go` | Ship files inside the binary; serve a sub-folder |
| `http.FileServerFS`, `ServeFileFS` | `webui.go` | Serve an `fs.FS` (Go 1.22+) |
| ServeMux patterns `GET /{$}` | `cexd` | Method in the pattern; `{$}` means exactly `/`, not "everything" |

---

## 8. Try it yourself

```bash
CEX_API_TOKENS='[{"token":"my-local-admin-token","user_id":"1","admin":true}]' make run
# open http://localhost:8080/  - the book and tape update live
# paste my-local-admin-token for balances, status and the order form

go test -race -v -run 'SlowBrowser|Shutdown' ./internal/marketdata
```

Try it with two tabs open, or freeze one in the browser's dev tools and
watch it get disconnected while the other keeps updating.

---

## 9. Check yourself

1. Why can't the engine write to WebSockets itself?
2. Why does a single shared Go channel not work for broadcasting?
3. What happens to a viewer whose buffer is full, and why is that kinder
   than letting it fall further behind?
4. Why does the hub poll every 100 ms instead of publishing every command?
5. Why does `cexd` need `RegisterOnShutdown` for streams?
6. Name two separate defences against HTML injection on the page.
7. Why is the spread computed with `BigInt`?

---

## 10. Next session (week 7)

**Operating it**: Prometheus metrics (queue depth, apply latency, fsync
time, subscribers, projection lag), load tests that report latency
percentiles rather than averages, and a Docker Compose stack that brings up
the exchange, PostgreSQL and the UI with one command.
