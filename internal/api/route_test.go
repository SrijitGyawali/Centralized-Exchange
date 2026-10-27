package api

import (
	"net/http/httptest"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// TestRouteNamesThePatternNotThePath: metrics label requests by route, so
// every order ID maps to the same pattern, and anything unmatched maps to ""
// (the caller turns that into one fixed label). Route never runs a handler
// and needs no token: it is a lookup, nothing more.
func TestRouteNamesThePatternNotThePath(t *testing.T) {
	e := engine.New(state.New(), engine.DefaultConfig()) // Route never touches the engine
	s := NewServer(e, nil, Config{})
	for _, tc := range []struct{ method, path, want string }{
		{"GET", "/healthz", "GET /healthz"},
		{"GET", "/readyz", "GET /readyz"},
		{"POST", "/v1/orders", "POST /v1/orders"},
		{"GET", "/v1/orders/42", "GET /v1/orders/{id}"},
		{"GET", "/v1/orders/98765", "GET /v1/orders/{id}"}, // same label for every ID
		{"DELETE", "/v1/orders/42", "DELETE /v1/orders/{id}"},
		{"GET", "/v1/requests/some-key", "GET /v1/requests/{key}"},
		{"GET", "/v1/status", "GET /v1/status"},
		{"GET", "/v1/no-such-route", ""},   // 404
		{"PUT", "/v1/orders", ""},          // 405: wrong method
		{"GET", "/wp-admin/setup.php", ""}, // a scanner's random path
	} {
		if got := s.Route(httptest.NewRequest(tc.method, tc.path, nil)); got != tc.want {
			t.Errorf("Route(%s %s) = %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}
