package telemetry

import (
	"net/http"
	"runtime"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/node"
)

// Histogram buckets, in seconds. Buckets decide which percentiles a
// histogram can answer precisely: a p99 between two bucket bounds is only
// known to lie somewhere between them. Each set is dense where this system's
// values actually fall (measured on the development machine; see
// evidence/bench), and coarse elsewhere.
var (
	// cpuBuckets: pure computation (apply, queries), 1 µs to 10 ms.
	cpuBuckets = []float64{1e-6, 2.5e-6, 5e-6, 10e-6, 25e-6, 50e-6, 100e-6, 250e-6, 500e-6, 1e-3, 2.5e-3, 5e-3, 10e-3}
	// diskBuckets: write and fsync, 50 µs to 1 s, densest around the
	// ~1.2 ms fsync measured in week 5.
	diskBuckets = []float64{50e-6, 100e-6, 250e-6, 500e-6, 750e-6, 1e-3, 1.25e-3, 1.5e-3, 2e-3, 3e-3, 5e-3, 7.5e-3, 10e-3, 25e-3, 50e-3, 100e-3, 250e-3, 1}
	// latencyBuckets: end-to-end waits (queue, engine, HTTP), 50 µs to 10 s.
	latencyBuckets = []float64{50e-6, 100e-6, 250e-6, 500e-6, 1e-3, 1.5e-3, 2e-3, 3e-3, 5e-3, 7.5e-3, 10e-3, 25e-3, 50e-3, 100e-3, 250e-3, 500e-3, 1, 2.5, 5, 10}
	// batchBuckets: commands per durable append (powers of two up to the
	// largest CEX_BATCH_SIZE allowed).
	batchBuckets = prometheus.ExponentialBuckets(1, 2, 11) // 1, 2, 4, … 1024
)

// Metrics owns one Prometheus registry and every metric cexd exports.
//
// A private registry (not prometheus.DefaultRegisterer) means two servers in
// one test process never collide, and nothing a dependency registers
// globally leaks into our /metrics.
type Metrics struct {
	reg *prometheus.Registry

	// engine (see engine.Observer for what each part means)
	queueWait prometheus.Histogram
	durable   prometheus.Histogram
	batch     prometheus.Histogram
	apply     *prometheus.HistogramVec // by command kind
	completed prometheus.Histogram
	query     prometheus.Histogram
	commands  *prometheus.CounterVec // by command kind and outcome
	fills     prometheus.Counter
	refused   *prometheus.CounterVec // by reason
	observer  *engineObserver        // the engine.Observer, children resolved once

	// journal
	journalWrite prometheus.Histogram
	journalSync  prometheus.Histogram
	journalBytes prometheus.Counter

	// HTTP
	httpDuration *prometheus.HistogramVec // by route and status code
	httpInFlight prometheus.Gauge
}

// New creates the registry with the Go runtime and process collectors and
// every cexd metric. version is the build's version string.
func New(version string) *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		// Heap, GC pauses and CPU, goroutines, scheduler latency: the Go
		// runtime's own health, from runtime/metrics.
		collectors.NewGoCollector(collectors.WithGoCollectorRuntimeMetrics(
			collectors.MetricsGC, collectors.MetricsScheduler)),
		// CPU seconds, resident memory, open file descriptors of the process.
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	f := promauto.With(reg) // creates AND registers each metric
	f.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "cex", Name: "build_info",
		Help: "Always 1; the labels say which build is running.",
	}, []string{"version", "go_version"}).WithLabelValues(version, runtime.Version()).Set(1)

	m := &Metrics{
		reg: reg,
		queueWait: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: "cex", Subsystem: "engine", Name: "queue_wait_seconds",
			Help:    "Time a request waited in the intake queue before the owner goroutine took it.",
			Buckets: latencyBuckets,
		}),
		durable: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: "cex", Subsystem: "engine", Name: "durable_seconds",
			Help:    "One journal append (encode, write, fsync) for a whole batch.",
			Buckets: diskBuckets,
		}),
		batch: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: "cex", Subsystem: "engine", Name: "batch_commands",
			Help:    "Commands sharing one durable append (group commit).",
			Buckets: batchBuckets,
		}),
		apply: f.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "cex", Subsystem: "engine", Name: "apply_seconds",
			Help:    "One command's state transition, snapshot publish and reply: pure engine time.",
			Buckets: cpuBuckets,
		}, []string{"command"}),
		completed: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: "cex", Subsystem: "engine", Name: "command_seconds",
			Help:    "Admission to reply for one command: queue wait + durable append + apply.",
			Buckets: latencyBuckets,
		}),
		query: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: "cex", Subsystem: "engine", Name: "query_seconds",
			Help:    "One read-only query run on the owner goroutine.",
			Buckets: cpuBuckets,
		}),
		commands: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: "cex", Subsystem: "engine", Name: "commands_total",
			Help: "Commands applied, by kind and outcome (accepted, duplicate, or the rejection reason).",
		}, []string{"command", "outcome"}),
		fills: f.NewCounter(prometheus.CounterOpts{
			Namespace: "cex", Subsystem: "engine", Name: "fills_total",
			Help: "Trades executed (one per maker order matched).",
		}),
		refused: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: "cex", Subsystem: "engine", Name: "refused_total",
			Help: "Requests turned away before admission, by reason. Overloaded means the queue was full.",
		}, []string{"reason"}),
		journalWrite: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: "cex", Subsystem: "journal", Name: "write_seconds",
			Help:    "Writing one batch of frames into the OS page cache.",
			Buckets: diskBuckets,
		}),
		journalSync: f.NewHistogram(prometheus.HistogramOpts{
			Namespace: "cex", Subsystem: "journal", Name: "fsync_seconds",
			Help:    "fsync after a write: waiting for the storage device.",
			Buckets: diskBuckets,
		}),
		journalBytes: f.NewCounter(prometheus.CounterOpts{
			Namespace: "cex", Subsystem: "journal", Name: "appended_bytes_total",
			Help: "Bytes appended to the journal since start.",
		}),
		httpDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "cex", Subsystem: "http", Name: "request_duration_seconds",
			Help:    "Server-side HTTP latency, by route pattern and status code.",
			Buckets: latencyBuckets,
		}, []string{"route", "code"}),
		httpInFlight: f.NewGauge(prometheus.GaugeOpts{
			Namespace: "cex", Subsystem: "http", Name: "requests_in_flight",
			Help: "HTTP requests being served right now.",
		}),
	}

	// Create every known label combination up front, at zero. A series
	// that does not exist yet is invisible to rate() and alerts, so "no
	// rejections so far" would look like "no data". The engine observer
	// also keeps the kind and kind×outcome children it creates.
	m.observer = newEngineObserver(m)
	for _, r := range refusalReasons {
		m.refused.WithLabelValues(r)
	}
	return m
}

// Registry exposes the registry, for tests and for registering extra
// collectors.
func (m *Metrics) Registry() *prometheus.Registry { return m.reg }

// Handler serves the registry in the Prometheus text format: GET /metrics.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{
		Registry: m.reg, // also count failed scrapes in promhttp_metric_handler_errors_total
	})
}

// RecordRecovery exports what startup recovery found: how much journal was
// replayed and how long it took. They are set once and never change, which
// is what a gauge is for. Recovery time grows with the journal; this is the
// number to watch before deciding when snapshots (Tier 2) are needed.
func (m *Metrics) RecordRecovery(r node.Recovery) {
	f := promauto.With(m.reg)
	f.NewGauge(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "recovery", Name: "commands",
		Help: "Commands replayed from the journal at startup.",
	}).Set(float64(r.Commands))
	f.NewGauge(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "recovery", Name: "truncated_bytes",
		Help: "Torn-tail bytes cut off the journal at startup (normally 0).",
	}).Set(float64(r.TruncatedBytes))
	f.NewGauge(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "recovery", Name: "duration_seconds",
		Help: "Wall time from opening the journal to the engine running.",
	}).Set(r.Took.Seconds())
}

// seconds converts for Observe. Prometheus convention: base units, so
// seconds and bytes, never milliseconds.
func seconds(d time.Duration) float64 { return d.Seconds() }
