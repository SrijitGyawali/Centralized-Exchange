package api

import (
	"fmt"
	"net/http"
)

// handleHealthz is the liveness probe: "is the process alive and serving
// HTTP?" It must not depend on PostgreSQL or any other dependency. An
// orchestrator restarts the process when liveness fails, and restarting a
// healthy engine because the database is briefly down would make things
// worse.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "ok")
}

// handleReadyz is the readiness probe: "should traffic be routed here?"
// A not-ready server keeps running but receives 503, e.g. while the WAL is
// still being replayed after a restart.
//
// It is a function that *returns* a handler (a closure), so the injected
// ReadinessFunc is captured without a global variable.
func handleReadyz(ready ReadinessFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if err := ready(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, "not ready: %v\n", err)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ready")
	}
}
