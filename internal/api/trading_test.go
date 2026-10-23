package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

func apiFixture(t *testing.T, cfg engine.Config) (*engine.Engine, func(string, string, string, string, string) *httptest.ResponseRecorder) {
	t.Helper()
	e := engine.New(state.New(), cfg)
	go e.Run()
	t.Cleanup(func() { e.Close(); <-e.Done() })
	h := NewServer(e, nil, Config{Tokens: map[string]Principal{"admin": {1, true}, "buyer": {2, false}, "seller": {3, false}, "alias": {2, false}}, Burst: 1000})
	return e, func(method, path, token, key, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if key != "" {
			r.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
}
func TestTradingOwnershipIdempotencyAndPrivacy(t *testing.T) {
	_, req := apiFixture(t, engine.DefaultConfig())
	check := func(w *httptest.ResponseRecorder, status int) {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status %d want %d: %s", w.Code, status, w.Body)
		}
	}
	check(req("POST", "/v1/admin/fund", "buyer", "fund", `{"user_id":"2","asset":"USDT","amount":"1000"}`), 403)
	check(req("POST", "/v1/admin/fund", "admin", "fund-b", `{"user_id":"2","asset":"USDT","amount":"1000"}`), 200)
	check(req("POST", "/v1/admin/fund", "admin", "fund-s", `{"user_id":"3","asset":"BTC","amount":"1"}`), 200)
	ask := `{"symbol":"BTC-USDT","type":"limit","side":"sell","time_in_force":"GTC","price":"100","quantity":"1"}`
	w := req("POST", "/v1/orders", "seller", "ask", ask)
	check(w, 200)
	var out resultDTO
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	oid := out.OrderID
	check(req("GET", "/v1/orders/"+oid, "buyer", "", ""), 404)
	check(req("DELETE", "/v1/orders/"+oid, "buyer", "foreign", ""), 404)
	check(req("GET", "/v1/requests/ask", "buyer", "", ""), 404)
	buy := strings.Replace(strings.Replace(ask, `"sell"`, `"buy"`, 1), `"quantity":"1"`, `"quantity":"0.4"`, 1)
	w = req("POST", "/v1/orders", "buyer", "buy", buy)
	check(w, 200)
	if strings.Contains(w.Body.String(), "maker") || strings.Contains(w.Body.String(), "taker") || !strings.Contains(w.Body.String(), "0.4000") {
		t.Fatal(w.Body.String())
	}
	w = req("POST", "/v1/orders", "alias", "buy", buy)
	check(w, 200)
	if !strings.Contains(w.Body.String(), `"duplicate":true`) {
		t.Fatal(w.Body.String())
	}
	check(req("POST", "/v1/orders", "buyer", "buy", strings.Replace(buy, "0.4", "0.5", 1)), 409)
	w = req("GET", "/v1/orders/"+oid, "seller", "", "")
	check(w, 200)
	if !strings.Contains(w.Body.String(), "0.6000") {
		t.Fatal(w.Body.String())
	}
	check(req("GET", "/v1/balances", "buyer", "", ""), 200)
	check(req("GET", "/v1/book", "buyer", "", ""), 200)
	check(req("GET", "/v1/requests/buy", "buyer", "", ""), 200)
	check(req("DELETE", "/v1/orders/"+oid, "seller", "cancel", ""), 200)
	check(req("GET", "/v1/orders/"+oid, "seller", "", ""), 404)
	check(req("POST", "/v1/orders", "seller", "poor", ask), 422)
	check(req("POST", "/v1/orders", "seller", "poor2", ask), 422)
}
func TestStrictJSONAndKeys(t *testing.T) {
	_, req := apiFixture(t, engine.DefaultConfig())
	for _, body := range []string{`null`, `[]`, `{}`, `{"user_id":"1","asset":"BTC","amount":"1","extra":"x"}`, `{"user_id":"1","asset":"BTC","amount":"1"} {}`, `{"user_id":"1","asset":"BTC","amount":true}`, `{"user_id":"01","asset":"BTC","amount":"1"}`, `{"user_id":"1","asset":"BTC","amount":"1e2"}`} {
		w := req("POST", "/v1/admin/fund", "admin", "k", body)
		if w.Code != 400 {
			t.Errorf("%s: %d %s", body, w.Code, w.Body)
		}
	}
	for _, key := range []string{"", strings.Repeat("a", 65), "bad\tkey"} {
		w := req("DELETE", "/v1/orders/1", "buyer", key, "")
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w := req("POST", "/v1/admin/fund", "admin", "long", `{"user_id":"1","asset":"BTC","amount":"`+strings.Repeat("1", maxBody)+`"}`)
	if w.Code != 413 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestBookSymbolAndCancelBody(t *testing.T) {
	_, req := apiFixture(t, engine.DefaultConfig())
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/v1/book?symbol=ETH-USDT", "", 400},
		{"GET", "/v1/book?symbol=BTC-USDT", "", 200},
		{"DELETE", "/v1/orders/1", `{}`, 400},
	} {
		w := req(tc.method, tc.path, "buyer", "k", tc.body)
		if w.Code != tc.status {
			t.Errorf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
}

type brokenJournal struct{}

func (brokenJournal) Append(command.Command) error { return errors.New("sync failed") }
func TestStorageFailureNeverAcknowledgesSuccess(t *testing.T) {
	_, req := apiFixture(t, engine.Config{Journal: brokenJournal{}})
	w := req("POST", "/v1/admin/fund", "admin", "fund", `{"user_id":"2","asset":"USDT","amount":"1000"}`)
	if w.Code != 503 || !strings.Contains(w.Body.String(), `"outcome_unknown":true`) {
		t.Fatal(w.Code, w.Body)
	}
	w = req("GET", "/v1/balances", "buyer", "", "")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
	w = req("GET", "/v1/book", "buyer", "", "")
	if w.Code != 503 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestAdmittedTimeoutAndAliasRateLimit(t *testing.T) {
	e := engine.New(state.New(), engine.DefaultConfig())
	h := NewServer(e, nil, Config{Tokens: map[string]Principal{"a": {1, false}, "b": {1, false}}, Burst: 1, RatePerSecond: .001, RequestTimeout: time.Millisecond})
	request := func(token, path, method string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "cancel")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request("a", "/v1/orders/1", "DELETE")
	if w.Code != 504 || !strings.Contains(w.Body.String(), "outcome_unknown") {
		t.Fatal(w.Code, w.Body)
	}
	w = request("b", "/v1/session", "GET")
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatal(w.Code, w.Body)
	}
	go e.Run()
	e.Close()
	<-e.Done()
}

func TestNoDefaultCredentialsAndMediaType(t *testing.T) {
	e := engine.New(state.New(), engine.DefaultConfig())
	go e.Run()
	defer func() { e.Close(); <-e.Done() }()
	h := NewServer(e, nil, Config{})
	r := httptest.NewRequest("GET", "/v1/session", nil)
	r.Header.Set("Authorization", "Bearer admin")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code, w.Body)
	}
	h = NewServer(e, nil, Config{Tokens: map[string]Principal{"admin": {1, true}}})
	r = httptest.NewRequest("POST", "/v1/admin/fund", strings.NewReader(`{"user_id":"1","asset":"BTC","amount":"1"}`))
	r.Header.Set("Authorization", "Bearer admin")
	r.Header.Set("Idempotency-Key", "k")
	r.Header.Set("Content-Type", "text/plain")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestOverloadHasKnownNonAdmission(t *testing.T) {
	e := engine.New(state.New(), engine.Config{QueueSize: 1})
	h := NewServer(e, nil, Config{Tokens: map[string]Principal{"t": {1, false}}, RequestTimeout: time.Millisecond})
	for i, want := range []int{504, 503} {
		r := httptest.NewRequest("DELETE", "/v1/orders/1", nil)
		r.Header.Set("Authorization", "Bearer t")
		r.Header.Set("Idempotency-Key", "k")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
		if i == 1 && strings.Contains(w.Body.String(), "outcome_unknown") {
			t.Fatal("rejected admission cannot be unknown")
		}
	}
	go e.Run()
	e.Close()
	<-e.Done()
}
