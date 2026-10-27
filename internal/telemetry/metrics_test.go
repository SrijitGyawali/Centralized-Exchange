package telemetry

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/marketdata"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/node"
)

// fakeProjection stands in for projector.Status.
type fakeProjection struct {
	seq      domain.Seq
	healthy  bool
	failures uint64
}

func (f *fakeProjection) ProjectedSeq() domain.Seq { return f.seq }
func (f *fakeProjection) Healthy() bool            { return f.healthy }
func (f *fakeProjection) Failures() uint64         { return f.failures }

// family returns one gathered metric family by name, or fails the test.
func family(t *testing.T, m *Metrics, name string) *dto.MetricFamily {
	t.Helper()
	families, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f
		}
	}
	t.Fatalf("metric %s not exported", name)
	return nil
}

// value returns the value of the single series in a gauge or counter family.
func value(t *testing.T, m *Metrics, name string) float64 {
	t.Helper()
	f := family(t, m, name)
	if len(f.Metric) != 1 {
		t.Fatalf("%s has %d series, want 1", name, len(f.Metric))
	}
	s := f.Metric[0]
	if s.Gauge != nil {
		return s.Gauge.GetValue()
	}
	return s.Counter.GetValue()
}

// newFull registers every source, the way cexd does.
func newFull(t *testing.T) (*Metrics, *fakeProjection) {
	t.Helper()
	m := New("v-test")
	m.RecordRecovery(node.Recovery{Commands: 1200, TruncatedBytes: 17, Took: 250 * time.Millisecond})
	proj := &fakeProjection{seq: 90, healthy: true, failures: 2}
	m.WatchProjection(proj, func() domain.Seq { return 100 })
	m.WatchHub(func() marketdata.Stats {
		return marketdata.Stats{Subscribers: 3, Published: 10, Delivered: 25, DeliveredBytes: 2500, SlowDrops: 1, Refused: 4}
	})
	m.WatchJournalFile(t.TempDir() + "/missing.wal") // a missing file reads as 0
	return m, proj
}

// TestEveryMetricFollowsPrometheusNamingRules runs promlint, the same
// checker `promtool check metrics` uses: counters end in _total, units are
// base units (seconds, bytes), names are snake_case, and so on.
func TestEveryMetricFollowsPrometheusNamingRules(t *testing.T) {
	m, _ := newFull(t)
	problems, err := testutil.GatherAndLint(m.Registry())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		if strings.HasPrefix(p.Metric, "cex_") { // only judge our own metrics
			t.Errorf("%s: %s", p.Metric, p.Text)
		}
	}
}

// TestHandlerServesTheTextFormat: what Prometheus actually scrapes.
func TestHandlerServesTheTextFormat(t *testing.T) {
	m, _ := newFull(t)
	w := httptest.NewRecorder()
	m.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(w.Result().Body)
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	for _, want := range []string{
		`cex_build_info{go_version="go`,
		`cex_engine_commands_total{command="place_order",outcome="insufficient_funds"} 0`, // pre-created at zero
		`cex_engine_refused_total{reason="overloaded"} 0`,
		"go_goroutines ",
		"go_sched_latencies_seconds_bucket", // runtime/metrics scheduler latency
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
}

func TestRecoveryProjectionAndHubValues(t *testing.T) {
	m, proj := newFull(t)
	for name, want := range map[string]float64{
		"cex_recovery_commands":                1200,
		"cex_recovery_truncated_bytes":         17,
		"cex_recovery_duration_seconds":        0.25,
		"cex_projection_enabled":               1,
		"cex_projection_projected_seq":         90,
		"cex_projection_lag_commands":          10,
		"cex_projection_healthy":               1,
		"cex_projection_failures_total":        2,
		"cex_marketdata_subscribers":           3,
		"cex_marketdata_published_total":       10,
		"cex_marketdata_delivered_total":       25,
		"cex_marketdata_delivered_bytes_total": 2500,
		"cex_marketdata_slow_drops_total":      1,
		"cex_marketdata_refused_total":         4,
		"cex_journal_file_bytes":               0,
	} {
		if got := value(t, m, name); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}

	// GaugeFuncs are read at scrape time: a change shows up on the next scrape.
	proj.seq, proj.healthy = 105, false
	if got := value(t, m, "cex_projection_lag_commands"); got != 0 {
		t.Errorf("lag with the projector ahead of the engine = %v, want 0 (clamped)", got)
	}
	if got := value(t, m, "cex_projection_healthy"); got != 0 {
		t.Errorf("healthy = %v after the projector failed, want 0", got)
	}
}

// TestDisabledProjectionIsDistinguishable: "disabled" exports enabled=0 and
// nothing else, so it can never be mistaken for "stuck at seq 0".
func TestDisabledProjectionIsDistinguishable(t *testing.T) {
	m := New("v-test")
	m.WatchProjection(nil, func() domain.Seq { return 5 })
	if got := value(t, m, "cex_projection_enabled"); got != 0 {
		t.Fatalf("enabled = %v, want 0", got)
	}
	families, _ := m.Registry().Gather()
	for _, f := range families {
		if strings.HasPrefix(f.GetName(), "cex_projection_") && f.GetName() != "cex_projection_enabled" {
			t.Errorf("disabled read model still exports %s", f.GetName())
		}
	}
}

// TestJournalOnSync records the split and the byte total.
func TestJournalOnSync(t *testing.T) {
	m := New("v-test")
	m.JournalOnSync(300, 20*time.Microsecond, 1200*time.Microsecond)
	m.JournalOnSync(100, 10*time.Microsecond, 900*time.Microsecond)
	if got := testutil.ToFloat64(m.journalBytes); got != 400 {
		t.Errorf("appended bytes = %v, want 400", got)
	}
	sync := family(t, m, "cex_journal_fsync_seconds").Metric[0].Histogram
	if sync.GetSampleCount() != 2 || sync.GetSampleSum() < 0.0020 || sync.GetSampleSum() > 0.0022 {
		t.Errorf("fsync histogram count %d sum %v, want 2 and 0.0021", sync.GetSampleCount(), sync.GetSampleSum())
	}
}
