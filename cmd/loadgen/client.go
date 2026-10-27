package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// client sends the exchange API's requests. One shared http.Client with a
// tuned Transport is used by every request goroutine.
type client struct {
	base    string // e.g. "http://127.0.0.1:8080"
	http    *http.Client
	runID   string // makes idempotency keys unique per run
	mu      sync.Mutex
	resting [][]string // per user: IDs of orders that rested and were not yet chosen for cancel
}

// newClient builds the client. The Transport matters for a load test:
// http.DefaultTransport keeps only 2 idle connections per host, so with
// hundreds of concurrent requests most would open a brand-new TCP connection
// and close it afterwards. That measures connection setup instead of the
// server, and on Windows can exhaust ephemeral ports (TIME_WAIT). Keeping as
// many idle connections as requests in flight reuses them.
func newClient(base string, maxInFlight int, timeout time.Duration, users int, runID string) *client {
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        maxInFlight,
		MaxIdleConnsPerHost: maxInFlight,
		IdleConnTimeout:     90 * time.Second,
	}
	return &client{
		base:    strings.TrimRight(base, "/"),
		http:    &http.Client{Transport: tr, Timeout: timeout},
		runID:   runID,
		resting: make([][]string, users),
	}
}

// response is the part of a result body the generator needs.
type response struct {
	OrderID string `json:"order_id"`
	Outcome string `json:"outcome"`
}

// do sends one request and returns the status code (0 if no response
// arrived) and the decoded body when it is JSON.
func (c *client) do(ctx context.Context, method, path, token, key, body string) (int, response, error) {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return 0, response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, response{}, err
	}
	defer resp.Body.Close()
	var out response
	// Read the whole body even when it is not needed: a connection can only
	// be reused after its body was consumed.
	data, err := io.ReadAll(resp.Body)
	if err == nil && len(data) > 0 && data[0] == '{' {
		_ = json.Unmarshal(data, &out)
	}
	return resp.StatusCode, out, err
}

// fund gives every user the workload's balances. The keys include the run
// ID, so each run adds fresh money; retries within the run stay idempotent.
func (c *client) fund(ctx context.Context, w Workload) error {
	for i := range w.Users {
		for _, a := range []struct {
			asset  string
			amount int64
		}{{"USDT", w.FundUSDT}, {"BTC", w.FundBTC}} {
			body := fmt.Sprintf(`{"user_id":"%d","asset":"%s","amount":"%d"}`, userID(i), a.asset, a.amount)
			key := fmt.Sprintf("lg-%s-fund-%d-%s", c.runID, i, a.asset)
			status, _, err := c.do(ctx, "POST", "/v1/admin/fund", adminToken(), key, body)
			if err != nil {
				return fmt.Errorf("funding user %d: %w", userID(i), err)
			}
			if status != http.StatusOK {
				return fmt.Errorf("funding user %d %s: HTTP %d (is the server started with `loadgen tokens` as CEX_API_TOKENS?)", userID(i), a.asset, status)
			}
		}
	}
	return nil
}

// send executes one op and reports what happened. seq numbers the op within
// the run, for its idempotency key.
func (c *client) send(ctx context.Context, op Op, seq int) (status int, err error) {
	key := fmt.Sprintf("lg-%s-%d", c.runID, seq)
	token := userToken(op.User)
	switch op.Kind {
	case OpCancel:
		id, ok := c.takeResting(op.User)
		if !ok {
			// Nothing of this user's is resting: cancel an ID that cannot
			// exist. The server answers 404 the same way as for a real but
			// already-filled order, so the request still costs a full trip
			// through the engine, which is what a cancel stream does.
			id = "1"
		}
		status, _, err = c.do(ctx, "DELETE", "/v1/orders/"+id, token, key, "")
		return status, err
	default:
		tif := "GTC"
		if op.Kind == OpCross {
			tif = "IOC"
		}
		body := fmt.Sprintf(`{"symbol":"BTC-USDT","type":"limit","side":"%s","time_in_force":"%s","price":"%d","quantity":"%s"}`,
			op.Side, tif, op.Price, lots(op.Qty))
		var resp response
		status, resp, err = c.do(ctx, "POST", "/v1/orders", token, key, body)
		if status == http.StatusOK && resp.Outcome == "rested" && resp.OrderID != "" {
			c.addResting(op.User, resp.OrderID)
		}
		return status, err
	}
}

func (c *client) addResting(user int, id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resting[user] = append(c.resting[user], id)
}

// takeResting removes and returns the user's OLDEST resting order. It may
// have been filled since it rested; then the server answers 404, which is a
// normal outcome for a cancel. A fixed rule (oldest first) keeps the choice
// deterministic for a given sequence of responses.
func (c *client) takeResting(user int) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.resting[user]) == 0 {
		return "", false
	}
	id := c.resting[user][0]
	c.resting[user] = c.resting[user][1:]
	return id, true
}

// lots formats a quantity in lots as BTC with 4 decimals: 25 -> "0.0025".
func lots(q int64) string { return fmt.Sprintf("%d.%04d", q/10_000, q%10_000) }
