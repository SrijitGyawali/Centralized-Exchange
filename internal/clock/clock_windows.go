//go:build windows

package clock

import (
	"syscall"
	"unsafe"
)

// QueryPerformanceCounter is kernel32's high-resolution counter: it ticks
// `freq` times per second (10 MHz on current Windows, so 100 ns), never goes
// backwards, and is consistent across CPUs. The standard library's syscall
// package can call it directly; no third-party module is needed.
var (
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	qpc      = kernel32.NewProc("QueryPerformanceCounter")
	qpf      = kernel32.NewProc("QueryPerformanceFrequency")
	freq     int64
)

func init() {
	if r, _, err := qpf.Call(uintptr(unsafe.Pointer(&freq))); r == 0 || freq <= 0 {
		panic("clock: QueryPerformanceFrequency failed: " + err.Error()) // cannot happen on Windows XP or later
	}
}

func now() int64 {
	var c int64
	qpc.Call(uintptr(unsafe.Pointer(&c))) // documented never to fail on XP and later
	// c * 1e9 / freq would overflow int64 after about 10 days of uptime at
	// 10 MHz, so convert whole seconds and the remainder separately.
	return c/freq*1e9 + c%freq*1e9/freq
}
