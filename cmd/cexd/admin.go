package main

import (
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/telemetry"
)

// adminHandler serves the operator endpoints on the PRIVATE admin listener
// (CEX_ADMIN_ADDR, loopback by default), never on the public API port:
//
//	GET /metrics              Prometheus scrape (internal/telemetry)
//	GET /debug/pprof/         index of runtime profiles
//	GET /debug/pprof/profile  30 s CPU profile (?seconds=N)
//	GET /debug/pprof/heap     live heap; also allocs, goroutine, block, mutex
//	GET /debug/pprof/trace    execution trace (?seconds=N): scheduling, GC, syscalls
//
// Why private: a CPU profile costs CPU while it runs, a heap profile can
// reveal what is in memory, and a goroutine dump shows internal structure.
// None of that belongs on the internet.
//
// Importing net/http/pprof also registers these handlers on
// http.DefaultServeMux as a side effect. cexd never serves DefaultServeMux,
// so that registration is unreachable; the explicit routes below are the
// only way in.
func adminHandler(m *telemetry.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	mux.HandleFunc("GET /debug/pprof/", pprof.Index) // also serves /debug/pprof/heap, /goroutine, …
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	// `go tool pprof` POSTs addresses to symbol. A method-less pattern would
	// CONFLICT with "GET /debug/pprof/" (each is more specific in a different
	// way: one by path, the other by method), and ServeMux panics on
	// ambiguous patterns, so the two methods are registered separately.
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}

// adminServer wraps the handler with timeouts suited to profiling: a CPU
// profile or trace streams for its whole duration, so the write timeout
// must exceed the longest profile an operator will ask for (2 minutes).
func adminServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      125 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}
