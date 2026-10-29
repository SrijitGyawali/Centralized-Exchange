package api

import "net/http"

// ReadinessFunc reports whether the server may accept trading traffic.
// It returns nil when ready, or an error explaining why not.
//
// The api package does not know *how* readiness is decided. main injects
// this function: cexd passes node.Ready, which reports a halted engine
// (after a storage or apply failure) as not ready.
type ReadinessFunc func() error

// NewRouter returns the public health handler. NewServer adds protected trading.
//
// It uses the standard library's http.ServeMux. Since Go 1.22, patterns can
// include the method and path wildcards ("GET /v1/orders/{id}"), which covers
// everything this service needs without a third-party router.
func NewRouter(ready ReadinessFunc) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /readyz", handleReadyz(ready))

	return mux
}
