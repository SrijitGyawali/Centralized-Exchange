package engine

import (
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
)

// Observer receives the engine's own measurements (week 7). The engine
// declares what it reports; package telemetry turns it into Prometheus
// metrics. The engine never imports a metrics library, and tests can pass a
// tiny fake.
//
// One command's time inside the engine splits into three parts, and each has
// its own method, because they have different causes and different fixes:
//
//	enqueue ─► QueueWait ─► Durable (shared by the batch) ─► Applied ─► reply
//	   └──────────────────────── Completed ─────────────────────────────┘
//
// What each part means:
//
//   - QueueWait: waiting for the owner goroutine. Grows when the engine is
//     saturated (fix: less work per command, or shed load).
//   - Durable: one journal write + fsync for the whole batch. Dominated by
//     the disk (fix: group commit, faster storage).
//   - Applied: the pure state transition plus publishing the snapshot. The
//     only part that is "the matching engine" in the narrow sense.
//
// Every method is called on the owner goroutine except Refused, which runs
// on the submitting goroutine. Implementations must be fast and safe for
// concurrent use; they must never block, because the owner goroutine waits
// for them.
type Observer interface {
	// QueueWait is how long one request (command or query) waited between
	// admission and the owner goroutine picking it up.
	QueueWait(d time.Duration)
	// Durable is one journal append covering n commands: encode, write and
	// fsync. With batching off, n is always 1.
	Durable(n int, d time.Duration)
	// Applied is one command's state transition, snapshot publish and
	// reply, with the result it produced.
	Applied(body command.Body, res command.Result, d time.Duration)
	// Completed is the whole time from admission until the reply was sent:
	// the engine-side latency a caller experiences.
	Completed(d time.Duration)
	// Query is one read-only query run on the owner goroutine.
	Query(d time.Duration)
	// Refused is a request turned away at admission (ErrOverloaded,
	// ErrClosed, ErrHalted). It never entered the queue.
	Refused(err error)
}

// nopObserver is used when Config.Observer is nil, so the hot path never
// needs an "if observer != nil" check.
type nopObserver struct{}

func (nopObserver) QueueWait(time.Duration)                             {}
func (nopObserver) Durable(int, time.Duration)                          {}
func (nopObserver) Applied(command.Body, command.Result, time.Duration) {}
func (nopObserver) Completed(time.Duration)                             {}
func (nopObserver) Query(time.Duration)                                 {}
func (nopObserver) Refused(error)                                       {}
