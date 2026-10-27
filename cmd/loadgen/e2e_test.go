package main

import (
	"bytes"
	"compress/gzip"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/api"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/telemetry"
)

// startExchange runs a real engine, API and metrics in-process, wired like
// cexd (without a journal: this test is about the load generator, not disk).
func startExchange(t *testing.T, users int) (apiURL, adminURL string) {
	t.Helper()
	m := telemetry.New("loadgen-test")
	e := engine.New(state.New(), engine.Config{Observer: m.Engine()})
	m.WatchEngine(e)
	go e.Run()
	t.Cleanup(func() { e.Close(); <-e.Done() })

	tokens := map[string]api.Principal{adminToken(): {User: adminUser, Admin: true}}
	for i := range users {
		tokens[userToken(i)] = api.Principal{User: domain.UserID(userID(i))}
	}
	s := api.NewServer(e, nil, api.Config{Tokens: tokens, RatePerSecond: 1e6, Burst: 1e6})
	pub := httptest.NewServer(m.InstrumentHTTP(s, func(r *http.Request) string {
		if p := s.Route(r); p != "" {
			return p
		}
		return telemetry.RouteUnmatched
	}))
	t.Cleanup(pub.Close)
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	adm := httptest.NewServer(mux)
	t.Cleanup(adm.Close)
	return pub.URL, adm.URL
}

// TestRunAgainstARealExchange runs the smoke workload end to end and checks
// the outputs: every arrival accounted for, answers and no errors, server
// stats attached, raw files present and consistent with the summary.
func TestRunAgainstARealExchange(t *testing.T) {
	w, err := LoadWorkload(smoke)
	if err != nil {
		t.Fatal(err)
	}
	pub, adm := startExchange(t, w.Users)
	out := t.TempDir()
	var stdout bytes.Buffer
	if err := run(t.Context(), options{workload: smoke, url: pub, admin: adm, out: out, label: "test", sameMachine: true}, &stdout); err != nil {
		t.Fatal(err)
	}

	var res Results
	b, err := os.ReadFile(filepath.Join(out, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &res); err != nil {
		t.Fatal(err)
	}
	if res.Warmup == nil || len(res.Stages) != 1 {
		t.Fatalf("warmup %v, %d stages", res.Warmup != nil, len(res.Stages))
	}
	s := res.Stages[0]
	// 50/s for 3 s: 150 arrivals exactly; the schedule is absolute.
	if s.Arrivals != 150 || s.Dropped != 0 || s.Errors != 0 || s.Answered != 150 {
		t.Errorf("stage = arrivals %d dropped %d errors %d answered %d; want 150/0/0/150 (by status %v)",
			s.Arrivals, s.Dropped, s.Errors, s.Answered, s.ByStatus)
	}
	if s.Latency.P50 <= 0 || s.Latency.P99 < s.Latency.P50 {
		t.Errorf("latency %+v", s.Latency)
	}
	if s.Server == nil || s.Server.EngineTotal.Count < 100 || s.Server.HTTP.Count < 100 || s.Server.Apply.Count < 100 {
		t.Fatalf("server stats missing or empty: %+v", s.Server)
	}
	if s.Server.Durable.Count != 0 {
		t.Errorf("durable count %v without a journal, want 0", s.Server.Durable.Count)
	}
	if res.Environment.ServerVersion != "loadgen-test" || res.Environment.ServerBatchLimit != 64 {
		t.Errorf("environment %+v", res.Environment)
	}

	// The raw CSV holds one row per arrival, warmup included.
	f, err := os.Open(filepath.Join(out, "requests.csv.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(zr).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if want := 1 + res.Warmup.Arrivals + s.Arrivals; len(rows) != want {
		t.Errorf("requests.csv.gz has %d rows, want %d (header + every arrival)", len(rows), want)
	}
	for _, name := range []string{"report.md", "timeline.csv", "metrics-end.prom"} {
		if fi, err := os.Stat(filepath.Join(out, name)); err != nil || fi.Size() == 0 {
			t.Errorf("%s missing or empty", name)
		}
	}
	if !bytes.Contains(stdout.Bytes(), []byte("## Server view")) {
		t.Error("the printed report lacks the server view")
	}
}
