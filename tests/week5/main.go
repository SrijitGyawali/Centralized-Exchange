// Command week5 exercises the public API against a real, crash-restarted cexd.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const admin = "week5-admin-token"
const seller = "week5-seller-token"

type gate struct {
	base   string
	client *http.Client
	cmd    *exec.Cmd
	done   chan error
	log    *os.File
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "week5 gate:", err)
		os.Exit(1)
	}
	fmt.Println("week5 process gate passed: funding, matching, queries, ownership, cancellation, crash recovery and idempotent retries")
}

func run() error {
	dir, err := os.MkdirTemp("", "cex-week5-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "cexd")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/cexd")
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build: %w\n%s", err, out)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		return err
	}
	g := &gate{base: "http://" + addr, client: &http.Client{Timeout: 3 * time.Second}}
	defer g.client.CloseIdleConnections()
	defer g.stop()
	start := func() error { return g.start(bin, dir, addr) }
	if err := start(); err != nil {
		return err
	}
	call := g.call
	if _, err := call("GET", "/v1/session", "", "", "", 401); err != nil {
		return err
	}
	if _, err := call("GET", "/v1/session", admin, "", "", 200); err != nil {
		return err
	}
	for _, f := range []struct{ key, body string }{
		{"fund-buyer", `{"user_id":"1","asset":"USDT","amount":"100000"}`},
		{"fund-seller", `{"user_id":"2","asset":"BTC","amount":"2"}`},
	} {
		if _, err := call("POST", "/v1/admin/fund", admin, f.key, f.body, 200); err != nil {
			return err
		}
	}
	askBody := `{"symbol":"BTC-USDT","type":"limit","side":"sell","time_in_force":"GTC","price":"100","quantity":"1"}`
	ask, err := call("POST", "/v1/orders", seller, "ask", askBody, 200)
	if err != nil {
		return err
	}
	id, ok := ask["order_id"].(string)
	if !ok || id == "" || id == "0" {
		return fmt.Errorf("missing string order_id: %v", ask)
	}
	buyBody := `{"symbol":"BTC-USDT","type":"limit","side":"buy","time_in_force":"IOC","price":"100","quantity":"0.4"}`
	buy, err := call("POST", "/v1/orders", admin, "buy", buyBody, 200)
	if err != nil {
		return err
	}
	fills, ok := buy["fills"].([]any)
	if !ok || len(fills) != 1 {
		return fmt.Errorf("expected one fill: %v", buy)
	}
	fill, ok := fills[0].(map[string]any)
	if !ok {
		return fmt.Errorf("invalid fill: %v", fills[0])
	}
	if err := field(fill, "quantity", "0.4000"); err != nil {
		return err
	}
	order, err := call("GET", "/v1/orders/"+id, seller, "", "", 200)
	if err != nil {
		return err
	}
	if err := field(order, "quantity", "0.6000"); err != nil {
		return err
	}
	if _, err := call("GET", "/v1/orders/"+id, admin, "", "", 404); err != nil {
		return err
	}
	if _, err := call("GET", "/v1/requests/ask", admin, "", "", 404); err != nil {
		return err
	}
	if _, err := call("DELETE", "/v1/orders/"+id, admin, "foreign-cancel", "", 404); err != nil {
		return err
	}
	book, err := call("GET", "/v1/book", admin, "", "", 200)
	if err != nil {
		return err
	}
	asks, ok := book["asks"].([]any)
	if !ok || len(asks) != 1 {
		return fmt.Errorf("expected resting ask: %v", book)
	}
	level, ok := asks[0].(map[string]any)
	if !ok {
		return fmt.Errorf("invalid ask level: %v", asks[0])
	}
	if err := field(level, "quantity", "0.6000"); err != nil {
		return err
	}
	if _, err := call("DELETE", "/v1/orders/"+id, seller, "cancel", "", 200); err != nil {
		return err
	}
	if _, err := call("GET", "/v1/orders/"+id, seller, "", "", 404); err != nil {
		return err
	}
	before := make([]map[string]any, 2)
	for i, token := range []string{admin, seller} {
		before[i], err = call("GET", "/v1/balances", token, "", "", 200)
		if err != nil {
			return err
		}
	}
	for i, expected := range []map[string]string{
		{"BTC": "0.4000", "USDT": "99960.000000"},
		{"BTC": "1.6000", "USDT": "40.000000"},
	} {
		rows, ok := before[i]["balances"].([]any)
		if !ok || len(rows) != 2 {
			return fmt.Errorf("unexpected balances: %v", before[i])
		}
		for _, row := range rows {
			balance, ok := row.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid balance row: %v", row)
			}
			asset, ok := balance["asset"].(string)
			want, known := expected[asset]
			if !ok || !known {
				return fmt.Errorf("unexpected asset: %v", balance)
			}
			if err := field(balance, "available", want); err != nil {
				return err
			}
			zero := "0.000000"
			if asset == "BTC" {
				zero = "0.0000"
			}
			if err := field(balance, "reserved", zero); err != nil {
				return err
			}
			delete(expected, asset)
		}
		if len(expected) != 0 {
			return fmt.Errorf("missing balance assets: %v", expected)
		}
	}
	// Kill only after acknowledged durable commands: replay must retain them.
	g.stop()
	if err := start(); err != nil {
		return err
	}
	for _, replay := range []struct {
		token, key, body string
		original         map[string]any
	}{
		{seller, "ask", askBody, ask}, {admin, "buy", buyBody, buy},
	} {
		got, err := call("POST", "/v1/orders", replay.token, replay.key, replay.body, 200)
		if err != nil {
			return err
		}
		if got["duplicate"] != true {
			return fmt.Errorf("retry was not duplicate: %v", got)
		}
		delete(got, "duplicate")
		delete(replay.original, "duplicate")
		if !reflect.DeepEqual(got, replay.original) {
			return fmt.Errorf("recovered result changed: got %v want %v", got, replay.original)
		}
		lookup, err := call("GET", "/v1/requests/"+replay.key, replay.token, "", "", 200)
		if err != nil {
			return err
		}
		delete(lookup, "duplicate")
		if !reflect.DeepEqual(lookup, replay.original) {
			return fmt.Errorf("request lookup differs: %v", lookup)
		}
	}
	for i, token := range []string{admin, seller} {
		after, err := call("GET", "/v1/balances", token, "", "", 200)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(before[i]["balances"], after["balances"]) {
			return fmt.Errorf("balances changed after recovery/retries: before %v after %v", before[i], after)
		}
		oldSeq, err := sequence(before[i])
		if err != nil {
			return err
		}
		newSeq, err := sequence(after)
		if err != nil {
			return err
		}
		if newSeq < oldSeq {
			return fmt.Errorf("sequence regressed: before %d after %d", oldSeq, newSeq)
		}
	}
	book, err = call("GET", "/v1/book", admin, "", "", 200)
	if err != nil {
		return err
	}
	for _, side := range []string{"asks", "bids"} {
		if levels, ok := book[side].([]any); !ok || len(levels) != 0 {
			return fmt.Errorf("unexpected %s after replay: %v", side, book)
		}
	}
	return nil
}

func field(m map[string]any, key, want string) error {
	if m[key] != want {
		return fmt.Errorf("%s: got %v want %q", key, m[key], want)
	}
	return nil
}

func sequence(m map[string]any) (uint64, error) {
	s, ok := m["seq"].(string)
	if !ok {
		return 0, fmt.Errorf("missing string sequence: %v", m)
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid sequence %q: %w", s, err)
	}
	return n, nil
}

func (g *gate) start(bin, dir, addr string) error {
	log, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		return err
	}
	g.log = log
	g.cmd = exec.Command(bin)
	override := map[string]string{"CEX_HTTP_ADDR": addr, "CEX_DATA_DIR": filepath.Join(dir, "wal"), "CEX_API_TOKENS": `[{"token":"` + admin + `","user_id":"1","admin":true},{"token":"` + seller + `","user_id":"2","admin":false}]`, "CEX_RATE_PER_SECOND": "1000", "CEX_RATE_BURST": "1000", "CEX_BATCH_SIZE": "64"}
	for _, env := range os.Environ() {
		key, _, _ := strings.Cut(env, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "CEX_") {
			g.cmd.Env = append(g.cmd.Env, env)
		}
	}
	for key, value := range override {
		g.cmd.Env = append(g.cmd.Env, key+"="+value)
	}
	g.cmd.Stdout, g.cmd.Stderr = log, log
	if err := g.cmd.Start(); err != nil {
		log.Close()
		g.cmd = nil
		return err
	}
	g.done = make(chan error, 1)
	cmd := g.cmd
	go func() { g.done <- cmd.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	lastProbe := "no response"
	for time.Now().Before(deadline) {
		select {
		case err := <-g.done:
			g.cmd = nil
			log.Close()
			data, _ := os.ReadFile(log.Name())
			return fmt.Errorf("server exited: %v\n%s", err, data)
		default:
		}
		resp, err := g.client.Get(g.base + "/readyz")
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			lastProbe = fmt.Sprintf("status %d: %s", resp.StatusCode, body)
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		} else {
			lastProbe = err.Error()
		}
		time.Sleep(30 * time.Millisecond)
	}
	g.stop()
	data, _ := os.ReadFile(log.Name())
	return fmt.Errorf("server readiness timeout (%s)\n%s", lastProbe, data)
}

func (g *gate) stop() {
	if g.cmd != nil {
		_ = g.cmd.Process.Kill()
		<-g.done
		g.cmd = nil
	}
	if g.log != nil {
		_ = g.log.Close()
		g.log = nil
	}
}

func (g *gate) call(method, path, token, key, body string, want int) (map[string]any, error) {
	req, err := http.NewRequest(method, g.base+path, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != want {
		return nil, fmt.Errorf("%s %s: status %d want %d: %s", method, path, resp.StatusCode, want, data)
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("%s %s JSON: %w", method, path, err)
	}
	return result, nil
}
