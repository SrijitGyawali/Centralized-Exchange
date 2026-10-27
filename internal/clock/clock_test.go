package clock

import (
	"slices"
	"testing"
	"time"
)

// TestResolutionIsFineEnoughForMicroseconds is the reason this package
// exists: two readings around a few microseconds of work must differ, and
// the smallest observable step must be far below a millisecond. With
// time.Now on Windows this test fails (every step is ~0.5 ms or more).
func TestResolutionIsFineEnoughForMicroseconds(t *testing.T) {
	var steps []time.Duration
	for range 1000 {
		a := Now()
		x := 0
		for i := range 2000 { // a couple of microseconds of work
			x += i * i
		}
		_ = x
		if d := Since(a); d > 0 {
			steps = append(steps, d)
		}
	}
	if len(steps) < 900 {
		t.Fatalf("only %d of 1000 short measurements were non-zero: the clock is too coarse", len(steps))
	}
	slices.Sort(steps)
	if median := steps[len(steps)/2]; median > 200*time.Microsecond {
		t.Errorf("median of a few µs of work measured as %s: resolution too coarse", median)
	}
}

func TestMonotonicAndConsistentWithSleep(t *testing.T) {
	prev := Now()
	for range 100_000 {
		n := Now()
		if n < prev {
			t.Fatalf("clock went backwards: %d after %d", n, prev)
		}
		prev = n
	}
	// time.Sleep is timed by the runtime's own (on Windows, coarse) clock, so
	// the precise clock may see a little less than 20 ms: allow a quarter.
	start := Now()
	time.Sleep(20 * time.Millisecond)
	if d := Since(start); d < 15*time.Millisecond || d > 2*time.Second {
		t.Errorf("a 20 ms sleep measured %s", d)
	}
	if got := start.Add(5 * time.Millisecond).Sub(start); got != 5*time.Millisecond {
		t.Errorf("Add/Sub round trip = %s", got)
	}
}

// BenchmarkNow: the cost of one reading. The engine reads the clock a few
// times per command, so this must stay well under a microsecond.
func BenchmarkNow(b *testing.B) {
	for b.Loop() {
		_ = Now()
	}
}

// BenchmarkTimeNow: the standard library, for comparison.
func BenchmarkTimeNow(b *testing.B) {
	for b.Loop() {
		_ = time.Now()
	}
}
