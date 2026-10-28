// Command demo is the three-minute demonstration (docs/DEMO.md), as a
// program: it builds the real cexd, runs it on a fresh data directory and
// narrates each step, so the same story can be shown live, recorded, or
// checked in CI.
//
//	make demo        # or: go run ./tests/demo
//
// Acts: scope; matching explained by the replay inspector; a lost response,
// a hard kill and a retry with exactly one effect; where the time goes; the
// limits.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	adminTok  = "demo-admin-token-0001"
	aliceTok  = "demo-alice-token-0001" // user 1: buys
	bobTok    = "demo-bob-token-00002"  // user 2: sells
	carolTok  = "demo-carol-token-0003" // user 3: sells
	tokensEnv = `[{"token":"` + adminTok + `","user_id":"100","admin":true},` +
		`{"token":"` + aliceTok + `","user_id":"1"},{"token":"` + bobTok + `","user_id":"2"},{"token":"` + carolTok + `","user_id":"3"}]`
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "\ndemo failed:", err)
		os.Exit(1)
	}
}

type demo struct {
	work, dataDir, bin, replay string
	base, admin                string
	cmd                        *exec.Cmd
	done                       chan error
}

func run() error {
	work, err := os.MkdirTemp("", "cex-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	d := &demo{work: work, dataDir: filepath.Join(work, "data")}
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	d.bin, d.replay = filepath.Join(work, "cexd"+exe), filepath.Join(work, "replay"+exe)
	for _, b := range [][2]string{{d.bin, "./cmd/cexd"}, {d.replay, "./cmd/replay"}} {
		if out, err := exec.Command("go", "build", "-o", b[0], b[1]).CombinedOutput(); err != nil {
			return fmt.Errorf("build %s: %w\n%s", b[1], err, out)
		}
	}
	defer d.kill()

	act("0:00", "Scope")
	say("A simulated BTC/USDT spot exchange in Go. One goroutine owns all trading state;")
	say("every command is written to a journal and fsynced BEFORE it is applied and answered;")
	say("money is exact integers in a double-entry ledger. Paper trading only.")
	if err := d.start(); err != nil {
		return err
	}
	say("cexd is running on a fresh data directory: %s", d.dataDir)

	act("0:30", "Matching, explained")
	for _, f := range []struct{ user, asset, amount string }{{"1", "USDT", "100000"}, {"2", "BTC", "1"}, {"3", "BTC", "1"}} {
		if _, err := d.call(adminTok, "POST", "/v1/admin/fund", "fund-"+f.user, fmt.Sprintf(`{"user_id":"%s","asset":"%s","amount":"%s"}`, f.user, f.asset, f.amount)); err != nil {
			return err
		}
	}
	say("Funded: alice 100,000 USDT; bob and carol 1 BTC each.")
	for _, o := range []struct{ tok, who, key, price string }{
		{bobTok, "bob", "bob-1", "60000"}, {carolTok, "carol", "carol-1", "60000"}, {bobTok, "bob", "bob-2", "59900"},
	} {
		r, err := d.call(o.tok, "POST", "/v1/orders", o.key, order("sell", o.price, "0.1000", "GTC"))
		if err != nil {
			return err
		}
		say("%-5s sells 0.1 BTC at %s -> order %s %s (seq %s)", o.who, o.price, r.OrderID, r.Outcome, r.Seq)
	}
	taker, err := d.call(aliceTok, "POST", "/v1/orders", "alice-1", order("buy", "60050", "0.2500", "GTC"))
	if err != nil {
		return err
	}
	say("alice buys 0.25 BTC, limit 60050 -> order %s %s with %d fills (seq %s)", taker.OrderID, taker.Outcome, len(taker.Fills), taker.Seq)
	say("The replay inspector recomputes that order from the journal alone:")
	if err := d.inspect("-explain", taker.OrderID); err != nil {
		return err
	}
	say("Price first (59900 before 60000), then time (bob's older order before carol's),")
	say("every fill at the MAKER's price, and the unused reservation released at the end.")

	act("1:15", "A lost response, a crash, and a retry")
	lost, err := d.call(aliceTok, "POST", "/v1/orders", "alice-2", order("buy", "59000", "0.0500", "GTC"))
	if err != nil {
		return err
	}
	say("alice places another order (key alice-2). Pretend the reply was LOST on the network:")
	say("she does not know whether it happened. Now the server is killed, hard (kill -9).")
	d.kill()
	if err := d.start(); err != nil {
		return err
	}
	rec, _ := d.recovered()
	say("Restarted: replayed %s commands from the journal in %s, digest %.16s...", rec["commands"], rec["took"], rec["digest"])
	retry, err := d.call(aliceTok, "POST", "/v1/orders", "alice-2", order("buy", "59000", "0.0500", "GTC"))
	if err != nil {
		return err
	}
	say("alice retries with the SAME key: duplicate=%v, seq %s, order %s (the original: seq %s, order %s)",
		retry.Duplicate, retry.Seq, retry.OrderID, lost.Seq, lost.OrderID)
	if !retry.Duplicate || retry.Seq != lost.Seq || retry.OrderID != lost.OrderID {
		return fmt.Errorf("the retry was not the original result: %+v vs %+v", retry, lost)
	}
	bal, err := d.balances(aliceTok)
	if err != nil {
		return err
	}
	say("Exactly one effect: alice's USDT reserved is %s (one 0.05 BTC order at 59000 = 2950 USDT).", bal["USDT"])

	act("2:00", "Where the time goes")
	say("A short burst: 400 orders from 16 concurrent clients (IOC buys at 50000: nothing to")
	say("match, so each one reserves USDT and releases it again, a full journaled command).")
	if err := d.burst(16, 25); err != nil {
		return err
	}
	metrics, err := d.get(d.admin + "/metrics")
	if err != nil {
		return err
	}
	for _, m := range []struct{ name, label string }{
		{"cex_http_request_duration_seconds", "HTTP (every request)"},
		{"cex_engine_command_seconds", "engine total"},
		{"cex_engine_queue_wait_seconds", "  queue wait"},
		{"cex_engine_durable_seconds", "  durable: write + fsync (per batch)"},
		{"cex_engine_apply_seconds", "  apply: the state machine"},
	} {
		sum, count := sumCount(metrics, m.name)
		if count > 0 {
			say("%-38s mean %8.3f ms over %d", m.label, sum/count*1000, int(count))
		}
	}
	if sum, count := sumCount(metrics, "cex_engine_batch_commands"); count > 0 {
		say("%-38s mean %8.1f commands per fsync", "group commit", sum/count)
	}
	say("Durability dominates: the engine waits on fsync. Under load, group commit shares one")
	say("fsync between queued commands: 3200 orders/s at a 2.4 ms median on this kind of laptop,")
	say("where one fsync per order saturates near 750/s (docs/PERFORMANCE.md).")

	act("2:40", "Limits, and the next decision")
	say("One symbol, one process: a machine loss needs replication, which is not built.")
	say("The latency tail is the disk's rare 100 ms-1 s fsyncs; memory grows ~370 B per command")
	say("because every idempotency key is kept. Next: snapshots and dedupe expiry, then replication.")
	say("Everything else: docs/LIMITATIONS.md.")
	return nil
}

func act(at, title string) {
	fmt.Printf("\n[%s] %s\n%s\n", at, title, strings.Repeat("-", len(title)+7))
}
func say(format string, a ...any) {
	fmt.Printf("  "+format+"\n", a...)
}

func order(side, price, qty, tif string) string {
	return fmt.Sprintf(`{"symbol":"BTC-USDT","type":"limit","side":"%s","time_in_force":"%s","price":"%s","quantity":"%s"}`, side, tif, price, qty)
}

func freeAddr() string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func (d *demo) start() error {
	addr, admin := freeAddr(), freeAddr()
	d.base, d.admin = "http://"+addr, "http://"+admin
	log, err := os.OpenFile(filepath.Join(d.work, "cexd.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	d.cmd = exec.Command(d.bin)
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !strings.HasPrefix(strings.ToUpper(k), "CEX_") {
			d.cmd.Env = append(d.cmd.Env, kv)
		}
	}
	d.cmd.Env = append(d.cmd.Env, "CEX_HTTP_ADDR="+addr, "CEX_ADMIN_ADDR="+admin, "CEX_DATA_DIR="+d.dataDir, "CEX_API_TOKENS="+tokensEnv,
		"CEX_RATE_PER_SECOND=100000", "CEX_RATE_BURST=100000") // the default 100/s per user would refuse the burst
	d.cmd.Stdout, d.cmd.Stderr = log, log
	if err := d.cmd.Start(); err != nil {
		return err
	}
	d.done = make(chan error, 1)
	go func(c *exec.Cmd, l *os.File) { d.done <- c.Wait(); l.Close() }(d.cmd, log)
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if resp, err := http.Get(d.base + "/readyz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
	}
	return fmt.Errorf("cexd never became ready")
}

func (d *demo) kill() {
	if d.cmd != nil {
		_ = d.cmd.Process.Kill()
		<-d.done
		d.cmd = nil
	}
}

type result struct {
	Seq       string `json:"seq"`
	Accepted  bool   `json:"accepted"`
	Duplicate bool   `json:"duplicate"`
	OrderID   string `json:"order_id"`
	Outcome   string `json:"outcome"`
	Fills     []any  `json:"fills"`
	Error     string `json:"error"`
}

func (d *demo) call(token, method, path, key, body string) (result, error) {
	req, err := http.NewRequest(method, d.base+path, strings.NewReader(body))
	if err != nil {
		return result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return result{}, err
	}
	defer resp.Body.Close()
	var r result
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return result{}, err
	}
	if resp.StatusCode != 200 || !r.Accepted {
		return r, fmt.Errorf("%s %s: HTTP %d %+v", method, path, resp.StatusCode, r)
	}
	return r, nil
}

// burst sends clients×each IOC orders concurrently, as alice.
func (d *demo) burst(clients, each int) error {
	errs := make(chan error, clients)
	for c := range clients {
		go func() {
			for i := range each {
				if _, err := d.call(aliceTok, "POST", "/v1/orders", fmt.Sprintf("burst-%d-%d", c, i), order("buy", "50000", "0.0010", "IOC")); err != nil {
					errs <- err
					return
				}
			}
			errs <- nil
		}()
	}
	for range clients {
		if err := <-errs; err != nil {
			return err
		}
	}
	return nil
}

func (d *demo) get(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func (d *demo) balances(token string) (map[string]string, error) {
	req, _ := http.NewRequest("GET", d.base+"/v1/balances", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var body struct {
		Balances []struct{ Asset, Reserved string } `json:"balances"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, b := range body.Balances {
		out[b.Asset] = b.Reserved
	}
	return out, nil
}

// inspect runs the replay inspector on the live data directory: it only
// reads the journal, so it is safe next to the running server.
func (d *demo) inspect(args ...string) error {
	out, err := exec.Command(d.replay, append([]string{"-data-dir", d.dataDir}, args...)...).CombinedOutput()
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		fmt.Println("    | " + line)
	}
	return err
}

// recovered reads the last "recovered" log line of cexd.
func (d *demo) recovered() (map[string]string, error) {
	f, err := os.Open(filepath.Join(d.work, "cexd.log"))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var line map[string]any
		if json.Unmarshal(sc.Bytes(), &line) == nil && line["msg"] == "recovered" {
			for k, v := range line {
				out[k] = fmt.Sprint(v)
			}
		}
	}
	return out, sc.Err()
}

// sumCount adds the _sum and _count samples of a histogram across labels,
// straight from the Prometheus text format.
func sumCount(text, name string) (sum, count float64) {
	for _, line := range strings.Split(text, "\n") {
		field := func(suffix string) (float64, bool) {
			if !strings.HasPrefix(line, name+suffix) {
				return 0, false
			}
			rest := strings.TrimPrefix(line, name+suffix)
			if rest == "" || (rest[0] != ' ' && rest[0] != '{') {
				return 0, false
			}
			v, err := strconv.ParseFloat(line[strings.LastIndexByte(line, ' ')+1:], 64)
			return v, err == nil
		}
		if v, ok := field("_sum"); ok {
			sum += v
		}
		if v, ok := field("_count"); ok {
			count += v
		}
	}
	return sum, count
}
