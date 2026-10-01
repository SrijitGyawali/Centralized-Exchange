package api

import "net/http"

// ReadinessFunc reports whether the server may accept trading traffic.
// It returns nil when ready, or an error explaining why not.
//
// The api package does not know *how* readiness is decided. main injects
// this function. Later it will check that replay is complete, the journal
// is writable and trading is not halted (see docs/ARCHITECTURE.md §7).
type ReadinessFunc func() error

// NewRouter returns the HTTP handler for the whole service.
//
// It uses the standard library's http.ServeMux. Since Go 1.22, patterns can
// include the method and path wildcards ("GET /v1/orders/{id}"), which covers
// everything this service needs without a third-party router.
func NewRouter(ready ReadinessFunc) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", handleReadyz(ready))

	// Week 5: POST /v1/orders, DELETE /v1/orders/{id}, GET /v1/orders/{id},
	// GET /v1/requests/{client_key}, GET /v1/balances, GET /v1/book.

	return mux
}
