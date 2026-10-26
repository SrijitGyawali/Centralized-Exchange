//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestProjectionOutageNeverStopsTrading is the week-6 read-model gate, run
// against the real server, real HTTP and a real PostgreSQL in Docker:
//
//  1. trade over HTTP; /v1/status shows the lag reach 0; SQL holds the rows
//  2. FREEZE the database (docker pause): trading continues at full speed,
//     /v1/status shows the lag growing
//  3. unfreeze it: the projector catches up to lag 0 and every command is in
//     SQL exactly once
//
// This is the plan's "projection outage" scenario: the read model may fall
// behind, but it never stops matching and never loses or doubles anything.
//
//	go test -tags=integration -run TestProjectionOutage ./cmd/cexd
func TestProjectionOutageNeverStopsTrading(t *testing.T) {
	ctx := context.Background()
	const password = "pw-week6-not-in-logs"
	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("cex"), postgres.WithUsername("cex"), postgres.WithPassword(password),
		postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("starting PostgreSQL (is Docker running?): %v", err)
	}
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(pg) })
	dbURL, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}

	addr, logs, stop := startServerWithEnv(t, map[string]string{
		"CEX_API_TOKENS": `[{"token":"week6-admin-token-01","user_id":"1","admin":true},` +
			`{"token":"week6-seller-token-1","user_id":"2"}]`,
		"CEX_DATABASE_URL": dbURL,
	})
	defer stop()
	const admin, seller = "week6-admin-token-01", "week6-seller-token-1"

	client := &http.Client{Timeout: 5 * time.Second}
	call := func(method, path, token, key, body string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(method, "http://"+addr+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	mustOK := func(method, path, token, key, body string) {
		t.Helper()
		if code, out := call(method, path, token, key, body); code != 200 {
			t.Fatalf("%s %s: %d %v", method, path, code, out)
		}
	}
	lag := func() (int, bool) {
		t.Helper()
		_, out := call("GET", "/v1/status", admin, "", "")
		p, _ := out["projection"].(map[string]any)
		var n int
		fmt.Sscan(fmt.Sprint(p["lag"]), &n)
		return n, p["enabled"] == true
	}
	waitForLagZero := func() {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			n, enabled := lag()
			if !enabled {
				t.Fatal("/v1/status says the projection is disabled")
			}
			if n == 0 {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("the projection never caught up; lag still %d", n)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	sqlCount := func(query string) int {
		t.Helper()
		conn, err := pgx.Connect(ctx, dbURL)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		var n int
		if err := conn.QueryRow(ctx, query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 1. Trade, then see SQL catch up.
	mustOK("POST", "/v1/admin/fund", admin, "fund-1", `{"user_id":"1","asset":"USDT","amount":"100000"}`)
	mustOK("POST", "/v1/admin/fund", admin, "fund-2", `{"user_id":"2","asset":"BTC","amount":"5"}`)
	mustOK("POST", "/v1/orders", seller, "ask", `{"symbol":"BTC-USDT","type":"limit","side":"sell","time_in_force":"GTC","price":"100","quantity":"1"}`)
	mustOK("POST", "/v1/orders", admin, "buy", `{"symbol":"BTC-USDT","type":"limit","side":"buy","time_in_force":"IOC","price":"100","quantity":"0.4"}`)
	waitForLagZero()
	if c, tr := sqlCount(`select count(*) from commands`), sqlCount(`select count(*) from trades`); c != 4 || tr != 1 {
		t.Fatalf("SQL has %d commands and %d trades, want 4 and 1", c, tr)
	}

	// 2. Freeze the database. Trading must not notice.
	if out, err := exec.Command("docker", "pause", pg.GetContainerID()).CombinedOutput(); err != nil {
		t.Fatalf("docker pause: %v %s", err, out)
	}
	paused := true
	defer func() {
		if paused {
			_ = exec.Command("docker", "unpause", pg.GetContainerID()).Run()
		}
	}()
	for i := range 5 {
		start := time.Now()
		mustOK("POST", "/v1/orders", seller, fmt.Sprint("outage-", i),
			`{"symbol":"BTC-USDT","type":"limit","side":"sell","time_in_force":"GTC","price":"200","quantity":"0.1"}`)
		if took := time.Since(start); took > 2*time.Second {
			t.Fatalf("an order took %s while the database was frozen: trading waited for SQL", took)
		}
	}
	if n, _ := lag(); n < 5 {
		t.Fatalf("lag = %d during the outage, want at least 5", n)
	}

	// 3. Unfreeze: the projector catches up, exactly once.
	if out, err := exec.Command("docker", "unpause", pg.GetContainerID()).CombinedOutput(); err != nil {
		t.Fatalf("docker unpause: %v %s", err, out)
	}
	paused = false
	waitForLagZero()
	if c := sqlCount(`select count(*) from commands`); c != 9 {
		t.Fatalf("SQL has %d commands after recovery, want exactly 9", c)
	}
	if o := sqlCount(`select count(*) from orders where status = 'open'`); o != 5 {
		t.Errorf("SQL has %d open orders, want the 5 placed during the outage", o)
	}
	if strings.Contains(logs.String(), password) {
		t.Error("the database password appeared in the server logs")
	}
}
