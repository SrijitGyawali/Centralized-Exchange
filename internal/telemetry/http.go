package telemetry

import (
	"net/http"
	"strconv"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/clock"
)

// RouteUnmatched is the single label used for every request that matches no
// route, so random paths cannot create new time series.
const RouteUnmatched = "unmatched"

// InstrumentHTTP wraps next so each request's latency is recorded by route
// and status code, and requests in flight are counted.
//
// routeOf names the route pattern for a request (for example
// "GET /v1/orders/{id}"). It must return a value from a small fixed set,
// never the raw path, and "" for requests that must not be timed:
// long-lived streams such as WebSockets would put hours-long "requests"
// into a latency histogram.
//
// This is server-side latency only: from the handler starting to it
// returning. Time in the client, the network and the kernel's accept queue
// is not included, which is why the load generator measures its own.
func (m *Metrics) InstrumentHTTP(next http.Handler, routeOf func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route := routeOf(r)
		if route == "" {
			next.ServeHTTP(w, r)
			return
		}
		m.httpInFlight.Inc()
		defer m.httpInFlight.Dec()
		rec := &statusRecorder{ResponseWriter: w}
		start := clock.Now()
		next.ServeHTTP(rec, r)
		m.httpDuration.WithLabelValues(route, strconv.Itoa(rec.code())).Observe(seconds(clock.Since(start)))
	})
}

// statusRecorder remembers the status code a handler sent. http.ResponseWriter
// has no way to ask for it afterwards, so the middleware wraps it.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code // only the first call counts, as in net/http
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK // Write without WriteHeader means 200
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the real writer (to flush, set
// deadlines or hijack) through this wrapper.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

func (s *statusRecorder) code() int {
	if s.status == 0 {
		return http.StatusOK // the handler wrote nothing: net/http sends 200
	}
	return s.status
}
