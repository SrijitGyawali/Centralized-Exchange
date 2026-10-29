// Package clock is a high-resolution monotonic clock for MEASURING
// durations. It is never used for wall-clock time or for anything
// the state machine decides: Apply stays pure.
//
// Why not time.Now? On Linux it is precise to nanoseconds. On Windows, Go's
// monotonic clock reads the kernel's interrupt time, which advances only on
// timer ticks: on the development machine every 578 µs (measured; see
// docs/PERFORMANCE.md). Every duration shorter than that read as 0 or
// 578 µs, so the engine's apply time, a few microseconds, was invisible,
// and fsync times were rounded to multiples of 0.58 ms. QueryPerformanceCounter
// has a 100 ns resolution, so this package uses it on Windows and time.Now
// everywhere else.
//
//	start := clock.Now()
//	work()
//	elapsed := clock.Since(start)
package clock

import "time"

// Instant is a reading of the monotonic clock: nanoseconds since an
// arbitrary origin. Only differences between two Instants mean anything.
type Instant int64

// Now reads the clock.
func Now() Instant { return Instant(now()) }

// Since returns the time elapsed since i.
func Since(i Instant) time.Duration { return time.Duration(now() - int64(i)) }

// Sub returns i - j.
func (i Instant) Sub(j Instant) time.Duration { return time.Duration(i - j) }

// Add returns i + d.
func (i Instant) Add(d time.Duration) Instant { return i + Instant(d) }
