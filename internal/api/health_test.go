package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// These tests use net/http/httptest: no real port and no network. The
// recorder captures status, headers and body so handlers are tested as plain
// functions.
func TestHealthAndReadiness(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		ready      ReadinessFunc
		wantStatus int
		wantBody   string
	}{
		{"healthz ok", http.MethodGet, "/healthz", nil, http.StatusOK, "ok"},
		{"readyz ready", http.MethodGet, "/readyz", func() error { return nil }, http.StatusOK, "ready"},
		{"readyz not ready", http.MethodGet, "/readyz", func() error { return errors.New("replaying") }, http.StatusServiceUnavailable, "not ready: replaying"},
		{"wrong method", http.MethodPost, "/healthz", nil, http.StatusMethodNotAllowed, ""},
		{"unknown path", http.MethodGet, "/nope", nil, http.StatusNotFound, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ready := tc.ready
			if ready == nil {
				ready = func() error { return nil }
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, nil)

			NewRouter(ready).ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}
