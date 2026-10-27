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
//
// Kinds and outcomes are ARRAYS indexed by constants, written with keyed
// elements ({kindFund: "fund"}), so an index and its label can never drift
// apart. The observer uses the indices on the hot path (see engineObserver).
const (
	kindPlace = iota
	kindCancel
	kindFund
	numKinds
)

const (
	outAccepted = iota
	outDuplicate
	outInvalid
	outInsufficientFunds
	outUnknownOrder
	outKeyConflict
	outSupplyCap
	numOutcomes
)

var (
	commandKinds = [numKinds]string{kindPlace: "place_order", kindCancel: "cancel_order", kindFund: "fund"}
	outcomes     = [numOutcomes]string{
		outAccepted: "accepted", outDuplicate: "duplicate", outInvalid: "invalid",
		outInsufficientFunds: "insufficient_funds", outUnknownOrder: "unknown_order",
		outKeyConflict: "key_conflict", outSupplyCap: "supply_cap",
	}
	refusalReasons = []string{"overloaded", "closed", "halted"}
)

// kindIndex returns the body's index in commandKinds, or -1 for a type the
// vocabulary does not know (impossible today: command.Body is sealed).
func kindIndex(b command.Body) int {
	switch b.(type) {
	case command.PlaceOrder:
		return kindPlace
	case command.CancelOrder:
		return kindCancel
	case command.Fund:
		return kindFund
	}
	return -1
}

// outcomeIndex returns the result's index in outcomes, or -1.
func outcomeIndex(r command.Result) int {
	if r.Duplicate {
		return outDuplicate
	}
	switch r.Rejected {
	case command.NotRejected:
		return outAccepted
	case command.RejectInvalid:
		return outInvalid
	case command.RejectInsufficientFunds:
		return outInsufficientFunds
	case command.RejectUnknownOrder:
		return outUnknownOrder
	case command.RejectKeyConflict:
		return outKeyConflict
	case command.RejectSupplyCap:
		return outSupplyCap
	}
	return -1
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
func (m *Metrics) Engine() engine.Observer { return m.observer }

// engineObserver is a separate type so Metrics itself does not grow
// QueueWait, Durable… methods that nobody else should call.
//
// It holds every labelled child metric ALREADY RESOLVED. Applied runs on the
// owner goroutine for every command; WithLabelValues would hash the label
// values and take a read lock to find the child each time (the week-7
// profile showed it costing as much as publishing the snapshot). Resolving
// once at startup turns that into two array lookups.
type engineObserver struct {
	m        *Metrics
	apply    [numKinds]prometheus.Observer
	commands [numKinds][numOutcomes]prometheus.Counter
}

// newEngineObserver creates every kind and kind×outcome series (at zero, so
// they exist before the first event) and keeps them.
func newEngineObserver(m *Metrics) *engineObserver {
	o := &engineObserver{m: m}
	for k, kname := range commandKinds {
		o.apply[k] = m.apply.WithLabelValues(kname)
		for oc, oname := range outcomes {
			o.commands[k][oc] = m.commands.WithLabelValues(kname, oname)
		}
	}
	return o
}

// The compiler checks the interface is satisfied, here, at build time.
var _ engine.Observer = (*engineObserver)(nil)

func (o *engineObserver) QueueWait(d time.Duration) { o.m.queueWait.Observe(seconds(d)) }

func (o *engineObserver) Durable(n int, d time.Duration) {
	o.m.durable.Observe(seconds(d))
	o.m.batch.Observe(float64(n))
}

func (o *engineObserver) Applied(body command.Body, res command.Result, d time.Duration) {
	k, oc := kindIndex(body), outcomeIndex(res)
	if k >= 0 && oc >= 0 {
		o.apply[k].Observe(seconds(d))
		o.commands[k][oc].Inc()
	} else { // outside the vocabulary: the slow path, under "other"
		kname, oname := "other", "other"
		if k >= 0 {
			kname = commandKinds[k]
		}
		if oc >= 0 {
			oname = outcomes[oc]
		}
		o.m.apply.WithLabelValues(kname).Observe(seconds(d))
		o.m.commands.WithLabelValues(kname, oname).Inc()
	}
	if !res.Duplicate { // a retry's fills happened the first time
		o.m.fills.Add(float64(len(res.Fills)))
	}
}

func (o *engineObserver) Completed(d time.Duration) { o.m.completed.Observe(seconds(d)) }
func (o *engineObserver) Query(d time.Duration)     { o.m.query.Observe(seconds(d)) }
func (o *engineObserver) Refused(err error)         { o.m.refused.WithLabelValues(refusal(err)).Inc() }

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
