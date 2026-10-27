package main

import (
	"context"
	"sync"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/clock"
)

// runner drives one load test.
//
// It is an OPEN workload: arrivals happen on a fixed schedule (rate R means
// one every 1/R seconds), whether or not earlier requests have finished.
// A CLOSED workload (N workers, each sending its next request when the last
// one returns) slows down when the server slows down, so it hides exactly
// the queueing a real overload causes; this is called "coordinated
// omission". Real users do not wait for each other before clicking.
//
// Two consequences, both deliberate:
//   - Latency is measured from the SCHEDULED arrival time, not from when the
//     request was finally sent, so lateness in the generator itself counts
//     against the result instead of being silently dropped.
//   - When MaxInFlight requests are already outstanding, a new arrival is
//     DROPPED and counted (like k6's dropped_iterations), never delayed. A
//     report with drops says plainly "this rate was not actually offered".
type runner struct {
	w      Workload
	c      *client
	gen    *Generator
	sem    chan struct{} // capacity MaxInFlight: one slot per outstanding request
	start  time.Time     // the run's time zero
	nextOp int           // op number, for idempotency keys (scheduler goroutine only)

	mu      sync.Mutex
	samples []sample
	wg      sync.WaitGroup
}

func newRunner(w Workload, c *client) *runner {
	return &runner{w: w, c: c, gen: NewGenerator(w), sem: make(chan struct{}, w.MaxInFlight)}
}

func (r *runner) record(s sample) {
	r.mu.Lock()
	r.samples = append(r.samples, s)
	r.mu.Unlock()
}

// stage offers rate arrivals per second for d. It returns when the last
// arrival has been scheduled; requests still in flight finish in the
// background and are recorded under this stage. Waiting for them here would
// pause the arrivals between stages, which an open workload must not do.
func (r *runner) stage(ctx context.Context, idx int, rate float64, d time.Duration) {
	// Two clocks: time.Time to SLEEP until each arrival (the runtime's timers
	// use it), and the high-resolution clock to MEASURE latency, because
	// time.Now on Windows only advances every ~0.5 ms (internal/clock).
	t0, c0 := time.Now(), clock.Now()
	for i := 0; ; i++ {
		// Absolute schedule: arrival i is due at t0 + i/rate. Computing each
		// time from t0 (not "previous + interval") means sleep overshoot
		// never accumulates: if the generator falls behind, the next due
		// arrivals fire immediately until it has caught up.
		due := time.Duration(float64(i) * float64(time.Second) / rate)
		if due >= d {
			return
		}
		at, atC := t0.Add(due), c0.Add(due)
		if wait := time.Until(at); wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		} else if ctx.Err() != nil {
			return
		}

		// The op is generated even if it is then dropped, so the offered
		// sequence is identical however the server performs.
		op, seq := r.gen.Next(), r.nextOp
		r.nextOp++
		select {
		case r.sem <- struct{}{}:
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				defer func() { <-r.sem }()
				status, _ := r.c.send(ctx, op, seq)
				r.record(sample{Stage: idx, Kind: op.Kind, Offset: at.Sub(r.start), Latency: clock.Since(atC), Status: status})
			}()
		default:
			r.record(sample{Stage: idx, Kind: op.Kind, Offset: at.Sub(r.start), Dropped: true})
		}
	}
}

// wait blocks until every request sent so far has finished.
func (r *runner) wait() { r.wg.Wait() }

// stageSamples returns the samples of one stage.
func (r *runner) stageSamples(idx int) []sample {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []sample
	for _, s := range r.samples {
		if s.Stage == idx {
			out = append(out, s)
		}
	}
	return out
}
