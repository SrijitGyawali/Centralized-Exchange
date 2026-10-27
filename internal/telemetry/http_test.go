package telemetry

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

// TestInstrumentHTTP checks the labels: the route PATTERN (not the path),
// the real status code (including "wrote a body without WriteHeader" = 200),
// one fixed label for unmatched paths, and no timing for skipped routes.
func TestInstrumentHTTP(t *testing.T) {
	m := New("v-test")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "0" {
			http.Error(w, "nope", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("ok")) // no WriteHeader: an implicit 200
	})
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(101) })
	routeOf := func(r *http.Request) string {
		_, pattern := mux.Handler(r)
		switch pattern {
		case "GET /stream":
			return "" // long-lived: not timed
		case "":
			return RouteUnmatched
		}
		return pattern
	}
	h := m.InstrumentHTTP(mux, routeOf)

	for _, path := range []string{"/v1/orders/1", "/v1/orders/2", "/v1/orders/0", "/random/path", "/another/one", "/stream"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
	}

	if n := testutil.CollectAndCount(m.httpDuration); n != 3 {
		t.Errorf("%d HTTP series, want 3: the stream must not be timed and paths must not become labels", n)
	}
	count := func(route, code string) uint64 {
		var out dto.Metric
		if err := m.httpDuration.WithLabelValues(route, code).(prometheus.Metric).Write(&out); err != nil {
			t.Fatal(err)
		}
		return out.GetHistogram().GetSampleCount()
	}
	for _, tc := range []struct {
		route, code string
		want        uint64
	}{
		{"GET /v1/orders/{id}", "200", 2}, // two different IDs, one series
		{"GET /v1/orders/{id}", "404", 1},
		{RouteUnmatched, "404", 2}, // two random paths, one series
	} {
		if got := count(tc.route, tc.code); got != tc.want {
			t.Errorf("requests{%s,%s} = %d, want %d", tc.route, tc.code, got, tc.want)
		}
	}
	if got := testutil.ToFloat64(m.httpInFlight); got != 0 {
		t.Errorf("in flight = %v after every request finished, want 0", got)
	}
}

// TestStatusRecorderKeepsTheFirstCode mirrors net/http: a second
// WriteHeader is ignored, so the recorded code is what the client got.
func TestStatusRecorderKeepsTheFirstCode(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	rec.WriteHeader(422)
	rec.WriteHeader(500)
	if rec.code() != 422 {
		t.Fatalf("code = %d, want 422", rec.code())
	}
	if _, ok := any(rec).(interface{ Unwrap() http.ResponseWriter }); !ok {
		t.Fatal("statusRecorder must expose Unwrap for http.ResponseController")
	}
}
