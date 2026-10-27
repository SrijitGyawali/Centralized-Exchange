package telemetry

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/marketdata"
)

// Projection is what the metrics need from the SQL read model's status
// (projector.Status provides it). Declared here, by the consumer.
type Projection interface {
	ProjectedSeq() domain.Seq
	Healthy() bool
	Failures() uint64
}

// WatchProjection exports the read model's progress. p may be nil when the
// read model is disabled; then only cex_projection_enabled (0) is exported,
// so a dashboard can tell "disabled" from "broken".
//
// Lag is applied minus projected, in commands. A small, steady lag is
// normal (the projector follows the journal a moment behind); a lag that
// keeps growing means the database is down or too slow (ADR-0009).
func (m *Metrics) WatchProjection(p Projection, applied func() domain.Seq) {
	f := promauto.With(m.reg)
	enabled := f.NewGauge(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "projection", Name: "enabled",
		Help: "1 if the PostgreSQL read model is configured.",
	})
	if p == nil {
		return
	}
	enabled.Set(1)
	f.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "projection", Name: "projected_seq",
		Help: "Seq of the last command committed to PostgreSQL.",
	}, func() float64 { return float64(p.ProjectedSeq()) })
	f.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "projection", Name: "lag_commands",
		Help: "Applied minus projected Seq: how far the read model is behind.",
	}, func() float64 {
		// The journal is written BEFORE a command is applied, so the
		// projector can be a moment ahead of the engine. Clamp to 0.
		a, pr := applied(), p.ProjectedSeq()
		if pr >= a {
			return 0
		}
		return float64(a - pr)
	})
	f.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "projection", Name: "healthy",
		Help: "1 if the projector is following the journal normally.",
	}, func() float64 {
		if p.Healthy() {
			return 1
		}
		return 0
	})
	f.NewCounterFunc(prometheus.CounterOpts{
		Namespace: "cex", Subsystem: "projection", Name: "failures_total",
		Help: "Failed projector starts or catch-ups (each followed by a retry).",
	}, func() float64 { return float64(p.Failures()) })
}

// WatchHub exports the market-data feed's totals.
func (m *Metrics) WatchHub(stats func() marketdata.Stats) {
	m.reg.MustRegister(newHubCollector(stats))
}

// hubCollector implements prometheus.Collector directly instead of using six
// separate CounterFuncs. Collect calls stats() ONCE per scrape, so all six
// values come from the same instant (one lock acquisition), and they can
// never disagree with each other.
type hubCollector struct {
	stats                                            func() marketdata.Stats
	subs, published, delivered, bytes, slow, refused *prometheus.Desc
}

func newHubCollector(stats func() marketdata.Stats) *hubCollector {
	d := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName("cex", "marketdata", name), help, nil, nil)
	}
	return &hubCollector{
		stats:     stats,
		subs:      d("subscribers", "Open WebSocket streams."),
		published: d("published_total", "Snapshots encoded (one per new Seq the hub saw)."),
		delivered: d("delivered_total", "Messages placed in subscriber buffers."),
		bytes:     d("delivered_bytes_total", "Bytes placed in subscriber buffers: the feed's bandwidth."),
		slow:      d("slow_drops_total", "Subscribers disconnected for being too slow."),
		refused:   d("refused_total", "Subscriptions refused (limit reached or shutting down)."),
	}
}

// Describe sends every Desc this collector can produce. DescribeByCollect
// derives them by running Collect once, which is fine for a cheap collector.
func (c *hubCollector) Describe(ch chan<- *prometheus.Desc) { prometheus.DescribeByCollect(c, ch) }

// Collect runs on every scrape.
func (c *hubCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.stats()
	ch <- prometheus.MustNewConstMetric(c.subs, prometheus.GaugeValue, float64(s.Subscribers))
	ch <- prometheus.MustNewConstMetric(c.published, prometheus.CounterValue, float64(s.Published))
	ch <- prometheus.MustNewConstMetric(c.delivered, prometheus.CounterValue, float64(s.Delivered))
	ch <- prometheus.MustNewConstMetric(c.bytes, prometheus.CounterValue, float64(s.DeliveredBytes))
	ch <- prometheus.MustNewConstMetric(c.slow, prometheus.CounterValue, float64(s.SlowDrops))
	ch <- prometheus.MustNewConstMetric(c.refused, prometheus.CounterValue, float64(s.Refused))
}
