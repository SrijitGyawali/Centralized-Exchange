package main

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// adminAddr finds the admin listener's address in the startup logs.
func adminAddr(t *testing.T, logs *syncBuffer) string {
	t.Helper()
	m := regexp.MustCompile(`"msg":"admin listening","addr":"([^"]+)"`).FindStringSubmatch(logs.String())
	if m == nil {
		t.Fatalf("no admin listener in the logs:\n%s", logs.String())
	}
	return m[1]
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// TestRunServesMetricsAndPprofOnlyOnTheAdminPort: the real server, wired as
// in production. A request through the public API shows up in /metrics with
// its route pattern, engine and journal metrics are live, pprof answers, and
// NONE of the operator endpoints exist on the public port.
func TestRunServesMetricsAndPprofOnlyOnTheAdminPort(t *testing.T) {
	addr, logs, stop := startServerWithEnv(t, map[string]string{
		"CEX_API_TOKENS": `[{"token":"metrics-admin-token-1","user_id":"1","admin":true}]`,
	})
	defer stop()
	admin := adminAddr(t, logs)

	req, _ := http.NewRequest("POST", "http://"+addr+"/v1/admin/fund", strings.NewReader(`{"user_id":"1","asset":"USDT","amount":"5"}`))
	req.Header.Set("Authorization", "Bearer metrics-admin-token-1")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "metrics-fund-1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("funding: %v %v", resp, err)
	}
	resp.Body.Close()
	get(t, "http://"+addr+"/v1/orders/123") // 401 without a token, but still labelled by its pattern

	code, body := get(t, "http://"+admin+"/metrics")
	if code != 200 {
		t.Fatalf("/metrics: %d", code)
	}
	for _, want := range []string{
		`cex_http_request_duration_seconds_count{code="200",route="POST /v1/admin/fund"} 1`,
		`cex_http_request_duration_seconds_count{code="401",route="GET /v1/orders/{id}"} 1`,
		`cex_engine_commands_total{command="fund",outcome="accepted"} 1`,
		"cex_engine_applied_seq 1",
		"cex_engine_durable_seconds_count 1",
		"cex_journal_fsync_seconds_count 1",
		"cex_projection_enabled 0",
		"cex_marketdata_subscribers 0",
		"cex_recovery_commands 0",
		"process_cpu_seconds_total",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q", want)
		}
	}
	if strings.Contains(body, "metrics-admin-token-1") {
		t.Error("/metrics exposes a token")
	}

	if code, body := get(t, "http://"+admin+"/debug/pprof/"); code != 200 || !strings.Contains(body, "goroutine") {
		t.Errorf("pprof index: %d", code)
	}
	if code, _ := get(t, "http://"+admin+"/debug/pprof/heap?debug=1"); code != 200 {
		t.Errorf("heap profile: %d", code)
	}
	for _, path := range []string{"/metrics", "/debug/pprof/", "/debug/pprof/heap"} {
		if code, _ := get(t, "http://"+addr+path); code == 200 {
			t.Errorf("public port serves %s: operator endpoints must stay private", path)
		}
	}
}

// TestAdminCanBeTurnedOff: CEX_ADMIN_ADDR=off starts no admin listener.
func TestAdminCanBeTurnedOff(t *testing.T) {
	_, logs, stop := startServerWithEnv(t, map[string]string{"CEX_ADMIN_ADDR": "off"})
	defer stop()
	if strings.Contains(logs.String(), "admin listening") {
		t.Fatal("admin listener started although CEX_ADMIN_ADDR=off")
	}
}

// TestHealthcheckSubcommand: `cexd healthcheck` succeeds against a ready
// server and fails once it has stopped.
func TestHealthcheckSubcommand(t *testing.T) {
	addr, _, stop := startServer(t)
	env := func(k string) string {
		if k == "CEX_HTTP_ADDR" {
			return addr
		}
		return ""
	}
	if err := healthcheck(env); err != nil {
		t.Fatalf("healthcheck against a ready server: %v", err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if err := healthcheck(env); err == nil {
		t.Fatal("healthcheck passed against a stopped server")
	}
}
