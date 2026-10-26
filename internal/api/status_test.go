package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// fakeProjection stands in for projector.Status: the API only needs two
// methods, so the test does not need a database.
type fakeProjection struct {
	seq     domain.Seq
	healthy bool
}

func (f fakeProjection) ProjectedSeq() domain.Seq { return f.seq }
func (f fakeProjection) Healthy() bool            { return f.healthy }

func getStatus(t *testing.T, proj ProjectionStatus) map[string]any {
	t.Helper()
	e := engine.New(state.New(), engine.DefaultConfig())
	go e.Run()
	defer func() { e.Close(); <-e.Done() }()
	h := NewServer(e, nil, Config{Tokens: map[string]Principal{"admin-token-0001": {User: 1, Admin: true}}, Projection: proj})

	for i, key := range []string{"f1", "f2", "f3"} { // three commands: applied_seq becomes 3
		r := httptest.NewRequest("POST", "/v1/admin/fund", strings.NewReader(`{"user_id":"1","asset":"USDT","amount":"1"}`))
		r.Header.Set("Authorization", "Bearer admin-token-0001")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("funding %d: %d %s", i, w.Code, w.Body.String())
		}
	}

	r := httptest.NewRequest("GET", "/v1/status", nil)
	r.Header.Set("Authorization", "Bearer admin-token-0001")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("/v1/status: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStatusShowsProjectionLag(t *testing.T) {
	out := getStatus(t, fakeProjection{seq: 1, healthy: true})
	proj := out["projection"].(map[string]any)
	if out["applied_seq"] != "3" || proj["projected_seq"] != "1" || proj["lag"] != "2" || proj["healthy"] != true {
		t.Errorf("status = %v; want applied 3, projected 1, lag 2, healthy", out)
	}
}

func TestStatusWithoutAReadModel(t *testing.T) {
	out := getStatus(t, nil)
	if proj := out["projection"].(map[string]any); proj["enabled"] != false {
		t.Errorf("status = %v; want the projection reported as disabled", out)
	}
}
