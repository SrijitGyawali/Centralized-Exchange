package telemetry

import (
	"errors"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
)

// Label values are fixed vocabularies, never free text: every distinct
// label value is a separate time series in Prometheus, so labels must have
// a small, known set of values.
var (
	commandKinds   = []string{"place_order", "cancel_order", "fund"}
	outcomes       = []string{"accepted", "duplicate", "invalid", "insufficient_funds", "unknown_order", "key_conflict", "supply_cap"}
	refusalReasons = []string{"overloaded", "closed", "halted"}
)

func kind(b command.Body) string {
	switch b.(type) {
	case command.PlaceOrder:
		return "place_order"
	case command.CancelOrder:
		return "cancel_order"
	case command.Fund:
		return "fund"
	}
	return "other"
}

func outcome(r command.Result) string {
	if r.Duplicate {
		return "duplicate"
	}
	switch r.Rejected {
	case command.NotRejected:
		return "accepted"
	case command.RejectInvalid:
		return "invalid"
	case command.RejectInsufficientFunds:
		return "insufficient_funds"
	case command.RejectUnknownOrder:
		return "unknown_order"
	case command.RejectKeyConflict:
		return "key_conflict"
	case command.RejectSupplyCap:
		return "supply_cap"
	}
	return "other"
}

func refusal(err error) string {
	switch {
	case errors.Is(err, engine.ErrOverloaded):
		return "overloaded"
	case errors.Is(err, engine.ErrClosed):
		return "closed"
	case errors.Is(err, engine.ErrHalted):
		return "halted"
	}
	return "other"
}

// Engine returns the engine.Observer that records into these metrics. Pass
// it as engine.Config.Observer.
func (m *Metrics) Engine() engine.Observer { return engineObserver{m} }

// engineObserver is a separate type so Metrics itself does not grow
// QueueWait, Durable… methods that nobody else should call.
type engineObserver struct{ m *Metrics }

// The compiler checks the interface is satisfied, here, at build time.
var _ engine.Observer = engineObserver{}

func (o engineObserver) QueueWait(d time.Duration) { o.m.queueWait.Observe(seconds(d)) }

func (o engineObserver) Durable(n int, d time.Duration) {
	o.m.durable.Observe(seconds(d))
	o.m.batch.Observe(float64(n))
}

func (o engineObserver) Applied(body command.Body, res command.Result, d time.Duration) {
	k := kind(body)
	o.m.apply.WithLabelValues(k).Observe(seconds(d))
	o.m.commands.WithLabelValues(k, outcome(res)).Inc()
	if !res.Duplicate { // a retry's fills happened the first time
		o.m.fills.Add(float64(len(res.Fills)))
	}
}

func (o engineObserver) Completed(d time.Duration) { o.m.completed.Observe(seconds(d)) }
func (o engineObserver) Query(d time.Duration)     { o.m.query.Observe(seconds(d)) }
func (o engineObserver) Refused(err error)         { o.m.refused.WithLabelValues(refusal(err)).Inc() }

// WatchEngine exports values read at scrape time: queue length and
// capacity (saturation), the applied Seq (progress), and whether the engine
// has halted. A GaugeFunc calls its function on every scrape, so these cost
// nothing between scrapes and are never stale.
func (m *Metrics) WatchEngine(e *engine.Engine) {
	f := promauto.With(m.reg)
	f.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "engine", Name: "queue_length",
		Help: "Admitted requests waiting for the owner goroutine.",
	}, func() float64 { return float64(e.QueueLen()) })
	f.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "engine", Name: "queue_capacity",
		Help: "How many requests may wait; when length reaches it, new ones are refused.",
	}, func() float64 { return float64(e.QueueCap()) })
	f.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "engine", Name: "batch_limit",
		Help: "Most commands one durable append may carry (CEX_BATCH_SIZE; 1 = no group commit).",
	}, func() float64 { return float64(e.BatchLimit()) })
	f.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "engine", Name: "applied_seq",
		Help: "Seq of the last applied command.",
	}, func() float64 { return float64(e.Snapshot().Seq) })
	f.NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "engine", Name: "halted",
		Help: "1 if the engine stopped trading after a storage or apply failure.",
	}, func() float64 {
		if e.Err() != nil {
			return 1
		}
		return 0
	})
}

// JournalOnSync records one durable append. Pass it as
// node.Config.JournalOnSync.
func (m *Metrics) JournalOnSync(bytes int, write, sync time.Duration) {
	m.journalWrite.Observe(seconds(write))
	m.journalSync.Observe(seconds(sync))
	m.journalBytes.Add(float64(bytes))
}

// WatchJournalFile exports the journal's size on disk, read at scrape time.
// Tier 1 has one growing file, so this is the disk-growth signal.
func (m *Metrics) WatchJournalFile(path string) {
	promauto.With(m.reg).NewGaugeFunc(prometheus.GaugeOpts{
		Namespace: "cex", Subsystem: "journal", Name: "file_bytes",
		Help: "Size of journal.wal on disk.",
	}, func() float64 {
		fi, err := os.Stat(path)
		if err != nil {
			return 0
		}
		return float64(fi.Size())
	})
}
