package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// scrape is one parsed read of the server's /metrics.
type scrape struct {
	at       time.Time
	families map[string]*dto.MetricFamily
	raw      []byte
}

// scraper reads the server's private metrics endpoint (CEX_ADMIN_ADDR).
type scraper struct {
	url  string // e.g. http://127.0.0.1:6060/metrics
	http *http.Client
}

func (s *scraper) get(ctx context.Context) (scrape, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.url, nil)
	if err != nil {
		return scrape{}, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return scrape{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return scrape{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return scrape{}, fmt.Errorf("GET %s: HTTP %d", s.url, resp.StatusCode)
	}
	// The same parser Prometheus uses for the text format.
	parser := expfmt.NewTextParser(model.UTF8Validation)
	fams, err := parser.TextToMetricFamilies(strings.NewReader(string(raw)))
	if err != nil {
		return scrape{}, fmt.Errorf("parsing %s: %w", s.url, err)
	}
	return scrape{at: time.Now(), families: fams, raw: raw}, nil
}

// labelsMatch reports whether a series has every label in want.
func labelsMatch(m *dto.Metric, want map[string]string) bool {
	for k, v := range want {
		found := false
		for _, lp := range m.GetLabel() {
			if lp.GetName() == k && lp.GetValue() == v {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// value sums every matching series of a counter or gauge.
func (s scrape) value(name string, want map[string]string) float64 {
	var total float64
	if f := s.families[name]; f != nil {
		for _, m := range f.GetMetric() {
			if !labelsMatch(m, want) {
				continue
			}
			switch {
			case m.Counter != nil:
				total += m.Counter.GetValue()
			case m.Gauge != nil:
				total += m.Gauge.GetValue()
			case m.Untyped != nil:
				total += m.Untyped.GetValue()
			}
		}
	}
	return total
}

// label returns the value of one label of the first series of a metric.
func (s scrape) label(name, key string) string {
	if f := s.families[name]; f != nil && len(f.GetMetric()) > 0 {
		for _, lp := range f.GetMetric()[0].GetLabel() {
			if lp.GetName() == key {
				return lp.GetValue()
			}
		}
	}
	return ""
}

// hist is a histogram's cumulative buckets, merged over matching series.
type hist struct {
	bounds []float64 // upper bounds, ascending, ending with +Inf
	counts []float64 // cumulative counts per bound
	count  float64
	sum    float64
}

func (s scrape) histogram(name string, want map[string]string) hist {
	byBound := map[float64]float64{}
	var h hist
	if f := s.families[name]; f != nil {
		for _, m := range f.GetMetric() {
			if m.Histogram == nil || !labelsMatch(m, want) {
				continue
			}
			h.count += float64(m.Histogram.GetSampleCount())
			h.sum += m.Histogram.GetSampleSum()
			hasInf := false
			for _, b := range m.Histogram.GetBucket() {
				byBound[b.GetUpperBound()] += float64(b.GetCumulativeCount())
				hasInf = hasInf || math.IsInf(b.GetUpperBound(), 1)
			}
			if !hasInf { // +Inf may be left implicit: it always equals the sample count
				byBound[math.Inf(1)] += float64(m.Histogram.GetSampleCount())
			}
		}
	}
	for b := range byBound {
		h.bounds = append(h.bounds, b)
	}
	slices.Sort(h.bounds)
	for _, b := range h.bounds {
		h.counts = append(h.counts, byBound[b])
	}
	return h
}

// minus returns the observations made between two scrapes of the same
// histogram: counters only grow, so the difference is exactly what happened
// in between (Prometheus's increase() does the same).
func (h hist) minus(earlier hist) hist {
	out := hist{bounds: h.bounds, count: h.count - earlier.count, sum: h.sum - earlier.sum}
	for i, b := range h.bounds {
		prev := 0.0
		if j := slices.Index(earlier.bounds, b); j >= 0 {
			prev = earlier.counts[j]
		}
		out.counts = append(out.counts, h.counts[i]-prev)
	}
	return out
}

// quantile estimates the q-quantile like PromQL's histogram_quantile: find
// the bucket holding the rank, then interpolate linearly inside it. The
// answer is only as precise as the buckets around it, which is why the
// server's buckets are dense where its latencies fall.
func (h hist) quantile(q float64) float64 {
	if h.count <= 0 || len(h.bounds) == 0 {
		return 0
	}
	rank := q * h.count
	lower, below := 0.0, 0.0
	for i, upper := range h.bounds {
		if h.counts[i] >= rank {
			if math.IsInf(upper, 1) {
				return lower // beyond the last finite bucket: its lower bound is all we know
			}
			inBucket := h.counts[i] - below
			if inBucket <= 0 {
				return upper
			}
			return lower + (upper-lower)*(rank-below)/inBucket
		}
		lower, below = upper, h.counts[i]
	}
	return lower
}

// HistSummary is a server-side latency distribution for one stage, in
// milliseconds (quantiles are bucket estimates).
type HistSummary struct {
	Count  float64 `json:"count"`
	MeanMs float64 `json:"mean_ms"`
	P50Ms  float64 `json:"p50_ms"`
	P99Ms  float64 `json:"p99_ms"`
}

func summarizeHist(h hist) HistSummary {
	if h.count <= 0 {
		return HistSummary{}
	}
	r := func(sec float64) float64 { return math.Round(sec*1e6) / 1e3 } // seconds → ms, µs precision
	return HistSummary{Count: h.count, MeanMs: r(h.sum / h.count), P50Ms: r(h.quantile(0.5)), P99Ms: r(h.quantile(0.99))}
}

// ServerStats is what the server measured about itself during one stage.
// Each level contains the next (see internal/telemetry): HTTP ⊃ engine total
// ⊃ queue wait + durable + apply.
type ServerStats struct {
	HTTP        HistSummary        `json:"http_orders_answered"` // order routes, answered codes only
	EngineTotal HistSummary        `json:"engine_total"`
	QueueWait   HistSummary        `json:"queue_wait"`
	Durable     HistSummary        `json:"durable_per_batch"`
	Apply       HistSummary        `json:"apply"`
	Fsync       HistSummary        `json:"fsync"`
	BatchMean   float64            `json:"batch_mean_commands"`
	Overloaded  float64            `json:"refused_overloaded"`
	Outcomes    map[string]float64 `json:"outcomes"`
	CPUCores    float64            `json:"cpu_cores"` // process CPU seconds per wall second
	MaxQueueLen float64            `json:"max_queue_length"`
	QueueCap    float64            `json:"queue_capacity"`
	MaxRSSMiB   float64            `json:"max_rss_mib"`
	MaxGorout   float64            `json:"max_goroutines"`
	MaxLag      float64            `json:"max_projection_lag"`
	SlowDrops   float64            `json:"ws_slow_drops"`
}

// The server's HTTP view is restricted to what the client side calls
// "answered": the order routes with a definite answer. Including the 503s
// of an overloaded server would make HTTP latency look BETTER under
// overload (a refusal takes microseconds), hiding the queueing.
var (
	orderRoutes   = []string{"POST /v1/orders", "DELETE /v1/orders/{id}"}
	answeredCodes = []string{"200", "404", "409", "422"}
)

// serverStats compares the scrapes at the start and end of a stage, and
// takes maxima over the 1-second samples taken during it.
func serverStats(start, end scrape, during []scrape) *ServerStats {
	d := func(name string, want map[string]string) hist {
		return end.histogram(name, want).minus(start.histogram(name, want))
	}
	var httpH hist
	for _, route := range orderRoutes {
		for _, code := range answeredCodes {
			httpH = mergeHist(httpH, d("cex_http_request_duration_seconds", map[string]string{"route": route, "code": code}))
		}
	}
	batch := d("cex_engine_batch_commands", nil)
	s := &ServerStats{
		HTTP:        summarizeHist(httpH),
		EngineTotal: summarizeHist(d("cex_engine_command_seconds", nil)),
		QueueWait:   summarizeHist(d("cex_engine_queue_wait_seconds", nil)),
		Durable:     summarizeHist(d("cex_engine_durable_seconds", nil)),
		Apply:       summarizeHist(d("cex_engine_apply_seconds", nil)),
		Fsync:       summarizeHist(d("cex_journal_fsync_seconds", nil)),
		Overloaded:  end.value("cex_engine_refused_total", map[string]string{"reason": "overloaded"}) - start.value("cex_engine_refused_total", map[string]string{"reason": "overloaded"}),
		Outcomes:    map[string]float64{},
		QueueCap:    end.value("cex_engine_queue_capacity", nil),
		SlowDrops:   end.value("cex_marketdata_slow_drops_total", nil) - start.value("cex_marketdata_slow_drops_total", nil),
	}
	if batch.count > 0 {
		s.BatchMean = math.Round(batch.sum/batch.count*10) / 10
	}
	for _, o := range []string{"accepted", "duplicate", "invalid", "insufficient_funds", "unknown_order", "key_conflict", "supply_cap"} {
		want := map[string]string{"outcome": o}
		if v := end.value("cex_engine_commands_total", want) - start.value("cex_engine_commands_total", want); v > 0 {
			s.Outcomes[o] = v
		}
	}
	if wall := end.at.Sub(start.at).Seconds(); wall > 0 {
		cpu := end.value("process_cpu_seconds_total", nil) - start.value("process_cpu_seconds_total", nil)
		s.CPUCores = math.Round(cpu/wall*100) / 100
	}
	for _, x := range append(during, end) {
		s.MaxQueueLen = max(s.MaxQueueLen, x.value("cex_engine_queue_length", nil))
		s.MaxRSSMiB = max(s.MaxRSSMiB, math.Round(x.value("process_resident_memory_bytes", nil)/(1<<20)*10)/10)
		s.MaxGorout = max(s.MaxGorout, x.value("go_goroutines", nil))
		s.MaxLag = max(s.MaxLag, x.value("cex_projection_lag_commands", nil))
	}
	return s
}

// mergeHist adds two histograms with the same bucket layout.
func mergeHist(a, b hist) hist {
	if a.count == 0 {
		return b
	}
	if b.count == 0 {
		return a
	}
	out := hist{bounds: a.bounds, count: a.count + b.count, sum: a.sum + b.sum}
	for i := range a.bounds {
		out.counts = append(out.counts, a.counts[i]+b.counts[i])
	}
	return out
}
