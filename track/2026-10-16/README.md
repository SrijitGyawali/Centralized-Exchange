# 16 October 2026: money as integers (`internal/domain`)

> **One-line summary:** the first real exchange code. We taught the system
> to turn strings like `"0.1"` and `"60000"` into exact integers, and to
> multiply them without ever overflowing, test-first, using only the
> standard library.

Plan reference: week 1, session 2. Decisions applied: ADR-0002 (units) and
ADR-0003 #5 (own parsers, "parse, don't validate").

---

## 1. What we built

```
internal/domain/
├── units.go          types Price/Qty/Atoms, constants, bounds, sentinel errors
├── parse.go          ParseQty("0.1") → 1000 lots, ParsePrice("60000") → 60000 ticks
├── parse_test.go     42 table cases + a fuzz test
├── notional.go       Notional(price, qty) → atoms, overflow-checked
└── notional_test.go  11 table cases + a runnable example
```

| Function | In → Out | Rejects |
|---|---|---|
| `ParseQty` | `"0.1"` → `Qty(1000)` | `""`, `"0"`, `"-1"`, `"0.00001"`, `"1e5"`, `" 1"`, `"01"`, `"10.0001"`… |
| `ParsePrice` | `"60000"` → `Price(60000)` | `"60000.5"`, `"0"`, `"1000001"`, `"6e4"`… |
| `Notional` | `(60000, 1000)` → `Atoms(6000000000)` = 6,000 USDT | zero/negative inputs, anything that would overflow `int64` |

---

## 2. Why each piece exists

### Why three different types instead of plain `int64`?

```go
type Price int64   // ticks
type Qty   int64   // lots
type Atoms int64   // quote money
```

With plain `int64`, nothing stops `balance - qty`, which is a nonsense
mix of units the compiler would happily accept. With named types it's a
**compile error**. The safety costs nothing at runtime: they're still just
`int64` underneath.

### Why parse strings by hand instead of `strconv.ParseFloat`?

```
float64("0.1") is really 0.1000000000000000055511151231257827...
```

Floats can't represent most decimals exactly. So `parseFixed` reads the
string **one digit at a time** with integer arithmetic:

```
"0.25" with 4 decimals:   0 → 2 → 25 → 250 → 2500   = Qty(2500)
```

### Why so strict? (`"01"`, `"0.10000"`, `" 1"` all rejected)

Each accepted string must map to **exactly one** value. That matters
later for idempotency: the same order must hash identically. Strictness
also turns client bugs (extra spaces, wrong precision) into clear errors
instead of silent surprises.

### Why check overflow *before* multiplying?

Go integers **wrap around silently**:

```go
var x int64 = math.MaxInt64
x = x + 1   // x is now -9223372036854775808. No panic, no error!
```

So the check is done *before* the operation, with a division that can't
overflow:

```go
if a > math.MaxInt64/b { return ErrOverflow }   // safe: decide first
return a * b, nil                                 // then multiply
```

### Why sentinel errors (`ErrPrecision`, `ErrOutOfRange`…)?

```go
return 0, fmt.Errorf("quantity %q: %w", s, ErrPrecision)
```

- The message is helpful for humans: `quantity "0.00001": too many decimal places`.
- `%w` *wraps* the sentinel, so code can still ask
  `errors.Is(err, domain.ErrPrecision)`. Later the API will map
  that to an HTTP 400 with a clear reason.

---

## 3. How we worked: test-driven development (red → green)

| Step | What happened |
|---|---|
| 🔴 **Red** | Wrote stub functions returning "not implemented", then wrote all the tests. Ran them: **44 failures**. That proves the tests can fail. |
| 🟢 **Green** | Implemented `parseFixed`, `ParseQty`, `ParsePrice`. Every test passed. |
| 🔴 → 🟢 | Same cycle for `Notional`: stub → tests fail → implement → pass. |
| 🔍 **Fuzz** | Ran the fuzzer for 20 s: **606,204 random inputs**, zero failures. |

Why write tests first? A test you've never seen fail might be testing
nothing. Watching it go red, then green, proves it really checks the code.

---

## 4. Go ideas met today

| Idea | Where | In one line |
|---|---|---|
| Named types | `type Qty int64` | Same memory as `int64`, but the compiler keeps units apart |
| Typed constants | `MaxQty Qty = 100_000` | `_` digit separators make big numbers readable |
| Sentinel errors | `var ErrPrecision = errors.New(...)` | Fixed error values that callers can compare against |
| Wrapping with `%w` | `fmt.Errorf("...: %w", err)` | Adds context while keeping `errors.Is` working |
| `strings.Cut` | `parse.go` | Splits around the first separator: `before, after, found` |
| Bytes vs runes | `isASCIIDigits` | `s[i]` is a byte; non-ASCII characters can never match `'0'..'9'` |
| Silent integer overflow | `appendDigit`, `mulPositive` | Go doesn't panic on overflow, so check first |
| Table-driven tests | both test files | One loop, many named cases, each run with `t.Run` |
| Fuzzing | `FuzzParseQty` | Go generates random inputs; failures get saved as regression tests |
| Differential testing | fuzz vs `math/big` | Compare against an independent exact implementation |
| Example tests | `ExampleNotional` | Documentation that `go test` actually runs and checks |
| Coverage | `go test -cover` | 96.6% of statements executed by tests |

---

## 5. Try it yourself

```bash
go test -v ./internal/domain                                  # see every case
go test -cover ./internal/domain                              # coverage
go test -run '^$' -fuzz=FuzzParseQty -fuzztime=30s ./internal/domain
go doc ./internal/domain                                      # the API
go doc ./internal/domain Notional                             # one function
```

---

## 6. Check yourself

1. Why does `ParsePrice("60000.0")` fail when the value is clearly a whole
   number?
2. What does `math.MaxInt64 + 1` evaluate to in Go at runtime, and why is
   that dangerous for money?
3. Why is the overflow check `a > MaxInt64/b` and not `a*b > MaxInt64`?
4. What does the fuzz test compare `ParseQty` against, and why is that
   useful?
5. 0.5 BTC at 50,000: how many atoms? (50,000 × 5,000 × 100 =
   25,000,000,000)

---

## 7. Next session (week 1, session 3)

The **order book**, `internal/book`: a FIFO price level (a queue of orders
at one price), sorted bids and asks, adding and cancelling orders. That's
week 1's gate: add and cancel five orders and print the expected book.
