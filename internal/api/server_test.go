package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

func TestProtectedSessionAndStrictBody(t *testing.T) {
	e := engine.New(state.New(), engine.DefaultConfig())
	go e.Run()
	defer func() { e.Close(); <-e.Done() }()
	h := NewServer(e, nil, Config{Tokens: map[string]Principal{"admin": {User: 1, Admin: true}}, Burst: 100})
	for _, tc := range []struct {
		method, path, token, body string
		status                    int
	}{
		{"GET", "/healthz", "", "", 200}, {"GET", "/v1/session", "", "", 401}, {"GET", "/v1/session", "admin", "", 200},
		{"POST", "/v1/admin/fund", "admin", `{"user_id":"1","asset":"BTC","amount":"1","amount":"2"}`, 400},
		{"POST", "/v1/admin/fund", "admin", `{"user_id":"1","asset":"BTC","amount":1}`, 400},
		{"POST", "/v1/admin/fund", "admin", `{"user_id":"1","asset":"BTC","amount":null}`, 400},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", "k")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Errorf("%s %s: %d %s", tc.method, tc.body, w.Code, w.Body.String())
		}
	}
}
