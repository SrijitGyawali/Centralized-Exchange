package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// request is one call the drill made, and what came back.
type request struct {
	User           int // index into the drill's users
	Method, Path   string
	Key, Body      string
	Status         int // 0 = no HTTP response at all
	Seq            uint64
	Duplicate      bool
	Accepted       bool
	OrderID        string // for a placement: the order it created
	Outcome        string // for a placement: rested, filled, …
	Answered       bool   // a definite result (the command was decided)
	Refused        bool   // 503 without outcome_unknown: never admitted, never ran
	UnknownOutcome bool   // no definite result: it may or may not have run
	Err            string // transport error, if any
}

// result is the part of the API's result body the drill checks.
type result struct {
	Seq            string `json:"seq"`
	Accepted       bool   `json:"accepted"`
	Duplicate      bool   `json:"duplicate"`
	OrderID        string `json:"order_id"`
	Outcome        string `json:"outcome"`
	Error          string `json:"error"`
	OutcomeUnknown bool   `json:"outcome_unknown"`
}

var client = &http.Client{
	Timeout:   5 * time.Second,
	Transport: &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 256},
}

// send performs one request and classifies the answer.
func send(ctx context.Context, base string, r request) request {
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, base+r.Path, body)
	if err != nil {
		r.Err, r.UnknownOutcome = err.Error(), true
		return r
	}
	token := userToken(r.User)
	if r.User < 0 {
		token = adminToken()
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if r.Key != "" && r.Method != http.MethodGet {
		req.Header.Set("Idempotency-Key", r.Key)
	}
	if r.Body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		// No response: the request may have been admitted, journaled and
		// applied before the connection died. Only a retry can tell.
		r.Err, r.UnknownOutcome = err.Error(), true
		return r
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	r.Status = resp.StatusCode
	var res result
	_ = json.Unmarshal(data, &res)
	switch {
	case res.Seq != "":
		r.Seq, _ = strconv.ParseUint(res.Seq, 10, 64)
		r.Answered, r.Accepted, r.Duplicate = true, res.Accepted, res.Duplicate
		r.OrderID, r.Outcome = res.OrderID, res.Outcome
	case resp.StatusCode == http.StatusServiceUnavailable && !res.OutcomeUnknown:
		r.Refused = true
	default:
		r.UnknownOutcome = true
		r.Err = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return r
}

// fund gives every drill user generous balances, as the admin.
func fund(ctx context.Context, base, run string) ([]request, error) {
	var out []request
	for i := range users {
		for _, a := range []struct{ asset, amount string }{{"USDT", "10000000"}, {"BTC", "10000"}} {
			r := send(ctx, base, request{User: -1, Method: "POST", Path: "/v1/admin/fund",
				Key:  fmt.Sprintf("%s-fund-%d-%s", run, i, a.asset),
				Body: fmt.Sprintf(`{"user_id":"%d","asset":"%s","amount":"%s"}`, userID(i), a.asset, a.amount)})
			if !r.Answered || !r.Accepted {
				return nil, fmt.Errorf("funding user %d %s: %+v", userID(i), a.asset, r)
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// traffic runs `clients` concurrent clients until ctx ends, each sending a
// seeded mix of resting orders, crossing orders and cancels of its own
// orders, and returns every request made. Keys are unique per run.
func traffic(ctx context.Context, base, run string, clients int, seed uint64) []request {
	var (
		mu  sync.Mutex
		all []request
		wg  sync.WaitGroup
	)
	for c := range clients {
		wg.Go(func() {
			rng := rand.New(rand.NewPCG(seed, uint64(c)))
			user := c % users
			var resting []string
			for n := 0; ctx.Err() == nil; n++ {
				r := request{User: user, Key: fmt.Sprintf("%s-c%02d-%06d", run, c, n)}
				switch p := rng.IntN(100); {
				case p < 20 && len(resting) > 0:
					r.Method, r.Path = "DELETE", "/v1/orders/"+resting[0]
					resting = resting[1:]
				case p < 60:
					side, price := "buy", 60_000-1-rng.IntN(50)
					if rng.IntN(2) == 0 {
						side, price = "sell", 60_000+1+rng.IntN(50)
					}
					r.Method, r.Path = "POST", "/v1/orders"
					r.Body = order(side, price, 1+rng.IntN(50), "GTC")
				default:
					side, price := "buy", 60_051
					if rng.IntN(2) == 0 {
						side, price = "sell", 59_949
					}
					r.Method, r.Path = "POST", "/v1/orders"
					r.Body = order(side, price, 1+rng.IntN(100), "IOC")
				}
				r = send(ctx, base, r)
				if r.Answered && r.Accepted && r.Outcome == "rested" {
					resting = append(resting, r.OrderID)
				}
				mu.Lock()
				all = append(all, r)
				mu.Unlock()
				if r.Err != "" && ctx.Err() == nil {
					time.Sleep(10 * time.Millisecond) // the server is gone: do not spin
				}
			}
		})
	}
	wg.Wait()
	return all
}

func order(side string, price, lots int, tif string) string {
	return fmt.Sprintf(`{"symbol":"BTC-USDT","type":"limit","side":"%s","time_in_force":"%s","price":"%d","quantity":"%d.%04d"}`,
		side, tif, price, lots/10_000, lots%10_000)
}

// lookup asks the server for the stored result of an earlier request.
func lookup(ctx context.Context, base string, r request) request {
	q := request{User: r.User, Method: "GET", Path: "/v1/requests/" + r.Key, Key: r.Key}
	return send(ctx, base, q)
}

// retry sends exactly the same request again, same key and body.
func retry(ctx context.Context, base string, r request) request {
	again := request{User: r.User, Method: r.Method, Path: r.Path, Key: r.Key, Body: r.Body}
	return send(ctx, base, again)
}

// parallel runs f over items with n workers and returns the results in order.
func parallel(items []request, n int, f func(request) request) []request {
	out := make([]request, len(items))
	var wg sync.WaitGroup
	next := make(chan int)
	for range n {
		wg.Go(func() {
			for i := range next {
				out[i] = f(items[i])
			}
		})
	}
	for i := range items {
		next <- i
	}
	close(next)
	wg.Wait()
	return out
}
