package book_test

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
)

// update rewrites golden files from the current output:
//
//	go test ./internal/book -run TestFixture -update
//
// Use it only after confirming the new output is RIGHT, then review the
// golden file's diff in git like any other code change.
var update = flag.Bool("update", false, "rewrite testdata/*.golden from the current output")

// TestFixtureReplaysIdentically is the matching gate: "a saved command fixture
// replays identically twice."
//
// It replays testdata/session.cmds into two fresh books, requires the two
// transcripts to be byte-for-byte identical (determinism), and compares them
// with testdata/session.golden (correctness). The golden file was written by
// hand, trade by trade, before this test first ran.
//
// This is "golden file" testing: the expected output lives in a reviewable
// file instead of inside the test code. The go tool ignores directories named
// testdata, so fixtures never end up in a build.
func TestFixtureReplaysIdentically(t *testing.T) {
	script, err := os.ReadFile("testdata/session.cmds")
	if err != nil {
		t.Fatal(err)
	}

	first := replay(t, string(script))
	second := replay(t, string(script))
	if first != second {
		t.Fatalf("two replays of the same fixture differ: matching is not deterministic\n%s",
			firstDifference(first, second))
	}

	const golden = "testdata/session.golden"
	if *update {
		if err := os.WriteFile(golden, []byte(first), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote %s", golden)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if first != string(want) {
		t.Errorf("replay differs from %s\n%s", golden, firstDifference(string(want), first))
	}
}

// replay runs every command in script against a new book and returns a
// human-readable transcript: each command, its outcome, its fills, and the
// final book. It assigns Seq the way the engine will, and checks the book's
// invariants after every command.
func replay(t *testing.T, script string) string {
	t.Helper()
	b := book.New()
	var out strings.Builder
	var seq domain.Seq

	for n, raw := range strings.Split(script, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		switch {
		case f[0] == "place" && len(f) == 7:
			o, tif, err := parsePlace(t, n+1, f)
			if err != nil { // rejected at the edge: never reaches the book, gets no Seq
				fmt.Fprintf(&out, "---     %s -> rejected: %v\n", line, err)
				continue
			}
			seq++
			o.Seq = seq
			res, err := b.Place(o, tif)
			if err != nil {
				fmt.Fprintf(&out, "seq %-3d %s -> rejected: %v\n", seq, line, err)
				break
			}
			outcome := res.Outcome.String()
			if res.Outcome != book.Filled {
				outcome += ", remaining " + domain.FormatQty(res.Remaining)
			}
			fmt.Fprintf(&out, "seq %-3d %s -> %s\n", seq, line, outcome)
			for _, fl := range res.Fills {
				fmt.Fprintf(&out, "        fill %s @ %s against #%d (user %d), maker left %s\n",
					domain.FormatQty(fl.Qty), domain.FormatPrice(fl.Price), fl.Maker, fl.MakerUser, domain.FormatQty(fl.MakerRemaining))
			}

		case f[0] == "cancel" && len(f) == 2:
			id := mustUint(t, n+1, f[1])
			seq++
			o, err := b.Cancel(domain.OrderID(id))
			if err != nil {
				fmt.Fprintf(&out, "seq %-3d %s -> rejected: %v\n", seq, line, err)
				break
			}
			fmt.Fprintf(&out, "seq %-3d %s -> canceled, remaining %s\n", seq, line, domain.FormatQty(o.Qty))

		default:
			t.Fatalf("fixture line %d: cannot understand %q", n+1, line)
		}

		if err := b.CheckInvariants(); err != nil {
			t.Fatalf("fixture line %d (%s): %v", n+1, line, err)
		}
	}

	fmt.Fprintf(&out, "\nfinal book:\n%v", b.Depth(0))
	return out.String()
}

// parsePlace turns "place <id> <user> <side> <price> <qty> <tif>" into an
// order. A bad price or quantity is returned as an error, the way the API
// will reject it. Anything else malformed is a bug in the fixture itself, so
// the test stops.
func parsePlace(t *testing.T, lineNo int, f []string) (book.Order, domain.TimeInForce, error) {
	t.Helper()
	o := book.Order{
		ID:   domain.OrderID(mustUint(t, lineNo, f[1])),
		User: domain.UserID(mustUint(t, lineNo, f[2])),
	}
	switch f[3] {
	case "buy":
		o.Side = domain.Buy
	case "sell":
		o.Side = domain.Sell
	default:
		t.Fatalf("fixture line %d: side %q", lineNo, f[3])
	}
	var tif domain.TimeInForce
	switch f[6] {
	case "gtc":
		tif = domain.GTC
	case "ioc":
		tif = domain.IOC
	default:
		t.Fatalf("fixture line %d: time in force %q", lineNo, f[6])
	}

	var err error
	if o.Price, err = domain.ParsePrice(f[4]); err != nil {
		return book.Order{}, 0, err
	}
	if o.Qty, err = domain.ParseQty(f[5]); err != nil {
		return book.Order{}, 0, err
	}
	return o, tif, nil
}

func mustUint(t *testing.T, lineNo int, s string) uint64 {
	t.Helper()
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		t.Fatalf("fixture line %d: %q is not a number", lineNo, s)
	}
	return v
}

// firstDifference shows the first line where two transcripts disagree, which
// is far easier to read than two full dumps.
func firstDifference(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(w), len(g)); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			return fmt.Sprintf("first difference at line %d:\n want: %q\n got:  %q", i+1, wl, gl)
		}
	}
	return "no difference"
}
