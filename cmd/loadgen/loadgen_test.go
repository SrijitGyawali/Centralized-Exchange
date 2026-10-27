package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/config"
)

const smoke = "../../loadtest/workloads/smoke.json"

// TestPercentilesUseNearestRank on 1..100 ms: the p99 of 100 values is the
// 99th smallest, p50 the 50th. No interpolation, no averaging.
func TestPercentilesUseNearestRank(t *testing.T) {
	var d []time.Duration
	for i := 100; i >= 1; i-- { // unsorted on purpose
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	p := percentiles(d)
	if p.P50 != 50 || p.P90 != 90 || p.P99 != 99 || p.P999 != 100 || p.Max != 100 {
		t.Fatalf("percentiles = %+v", p)
	}
	if (percentiles(nil) != Percentiles{}) {
		t.Fatal("no samples must give zeros, not a panic")
	}
}

// TestHistogramQuantileInterpolates like PromQL's histogram_quantile.
func TestHistogramQuantileInterpolates(t *testing.T) {
	// 100 observations: 50 in (0, 1], 40 in (1, 2], 10 in (2, 4].
	h := hist{bounds: []float64{1, 2, 4, math.Inf(1)}, counts: []float64{50, 90, 100, 100}, count: 100, sum: 150}
	for q, want := range map[float64]float64{0.25: 0.5, 0.5: 1, 0.7: 1.5, 0.95: 3} {
		if got := h.quantile(q); math.Abs(got-want) > 1e-9 {
			t.Errorf("quantile(%v) = %v, want %v", q, got, want)
		}
	}
	// Subtracting an earlier scrape leaves only what happened in between.
	earlier := hist{bounds: h.bounds, counts: []float64{50, 50, 50, 50}, count: 50, sum: 25}
	d := h.minus(earlier)
	if d.count != 50 || d.counts[0] != 0 || d.counts[1] != 40 || d.quantile(0.5) != 1.625 {
		t.Errorf("minus = %+v, median %v", d, d.quantile(0.5))
	}
}

// TestGeneratorIsDeterministic: same seed, same ops; the mix holds.
func TestGeneratorIsDeterministic(t *testing.T) {
	w, err := LoadWorkload(smoke)
	if err != nil {
		t.Fatal(err)
	}
	a, b := NewGenerator(w), NewGenerator(w)
	counts := map[OpKind]int{}
	const n = 20_000
	for range n {
		x, y := a.Next(), b.Next()
		if x != y {
			t.Fatalf("same seed diverged: %+v vs %+v", x, y)
		}
		counts[x.Kind]++
		switch x.Kind {
		case OpRest: // never crosses the mid: it must rest
			if (x.Side == "buy" && x.Price >= w.MidPrice) || (x.Side == "sell" && x.Price <= w.MidPrice) || x.Qty < 1 || x.Qty > w.MaxQtyLots {
				t.Fatalf("bad resting order %+v", x)
			}
		case OpCross: // reaches past every resting order on the other side
			if (x.Side == "buy" && x.Price <= w.MidPrice+w.SpreadTicks) || (x.Side == "sell" && x.Price >= w.MidPrice-w.SpreadTicks) {
				t.Fatalf("crossing order does not cross %+v", x)
			}
		}
	}
	total := w.Mix.Rest + w.Mix.Cross + w.Mix.Cancel
	for kind, weight := range map[OpKind]int{OpRest: w.Mix.Rest, OpCross: w.Mix.Cross, OpCancel: w.Mix.Cancel} {
		want := float64(n*weight) / float64(total)
		if got := float64(counts[kind]); math.Abs(got-want)/want > 0.05 {
			t.Errorf("%s: %v of %d, want about %v", kind, got, n, want)
		}
	}
	other := w
	other.Seed++
	g1, g2 := NewGenerator(w), NewGenerator(other)
	same := 0
	for range 10 {
		if g1.Next() == g2.Next() {
			same++
		}
	}
	if same == 10 {
		t.Error("a different seed produced the same ops")
	}
}

// TestWorkloadFilesAreValid: every committed workload loads, and typos fail.
func TestWorkloadFilesAreValid(t *testing.T) {
	files, _ := filepath.Glob("../../loadtest/workloads/*.json")
	if len(files) < 4 {
		t.Fatalf("found %d workload files", len(files))
	}
	for _, f := range files {
		if _, err := LoadWorkload(f); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	bad := filepath.Join(t.TempDir(), "typo.json")
	os.WriteFile(bad, []byte(`{"name":"x","max_in_fligth":5}`), 0o644)
	if _, err := LoadWorkload(bad); err == nil || !strings.Contains(err.Error(), "max_in_fligth") {
		t.Errorf("a misspelled field must be an error, got %v", err)
	}
}

// TestTokensAreAcceptedByTheServerConfig: the printed CEX_API_TOKENS must
// pass cexd's own strict parser, or every load test would start with 401s.
func TestTokensAreAcceptedByTheServerConfig(t *testing.T) {
	s, err := tokensJSON(50)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(func(k string) string {
		if k == "CEX_API_TOKENS" {
			return s
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Tokens) != 51 || !cfg.Tokens[adminToken()].Admin || cfg.Tokens[userToken(49)].User != 1050 {
		t.Fatalf("parsed %d tokens: %+v", len(cfg.Tokens), cfg.Tokens[userToken(49)])
	}
}

func TestSmallHelpers(t *testing.T) {
	for q, want := range map[int64]string{1: "0.0001", 25: "0.0025", 10_000: "1.0000", 123_456: "12.3456"} {
		if got := lots(q); got != want {
			t.Errorf("lots(%d) = %q, want %q", q, got, want)
		}
	}
	for in, want := range map[string]string{
		"http://127.0.0.1:8080":  "ws://127.0.0.1:8080/stream/book",
		"http://localhost:8080/": "ws://localhost:8080/stream/book",
		"https://cex.example":    "wss://cex.example/stream/book",
	} {
		if got := streamURL(in); got != want {
			t.Errorf("streamURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestComposeEnvMatchesTheTokens: deploy/cexd.env gives the Compose server
// exactly the credentials this tool uses, so `make up` + `make load` works.
// If this fails, regenerate the file (see its header).
func TestComposeEnvMatchesTheTokens(t *testing.T) {
	b, err := os.ReadFile("../../deploy/cexd.env")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := tokensJSON(50)
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if v, ok := strings.CutPrefix(line, "CEX_API_TOKENS="); ok {
			if v != want {
				t.Fatal("deploy/cexd.env CEX_API_TOKENS differs from `loadgen tokens` for 50 users")
			}
			return
		}
	}
	t.Fatal("deploy/cexd.env has no CEX_API_TOKENS line")
}
