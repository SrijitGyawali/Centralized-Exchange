//go:build !windows

package clock

import "time"

// origin anchors the readings. time.Since uses the monotonic part of a
// time.Time, which on Linux and macOS has nanosecond resolution.
var origin = time.Now()

func now() int64 { return int64(time.Since(origin)) }
