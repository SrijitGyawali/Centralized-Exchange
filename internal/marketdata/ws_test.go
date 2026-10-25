package marketdata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/command"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/state"
)

// startFeed runs a real engine, a hub reading its snapshots, and an HTTP
// test server serving the stream. It returns the engine and the ws:// URL.
func startFeed(t *testing.T, cfg Config) (*engine.Engine, *Hub, string) {
	t.Helper()
	e := engine.New(state.New(), engine.DefaultConfig())
	go e.Run()
	t.Cleanup(func() { e.Close(); <-e.Done() })

	h := New(e.Snapshot, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(cancel)

	srv := httptest.NewServer(h.Handler())
	t.Cleanup(srv.Close)
	return e, h, "ws" + strings.TrimPrefix(srv.URL, "http")
}

type wireMsg struct {
	Seq  string
	Bids []struct{ Price, Quantity string }
}

func read(t *testing.T, c *websocket.Conn) wireMsg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("reading the stream: %v", err)
	}
	var m wireMsg
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func submit(t *testing.T, e *engine.Engine, user domain.UserID, key string, body command.Body) command.Result {
	t.Helper()
	r, err := e.Submit(context.Background(), user, key, body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestStreamDeliversTheLiveBook(t *testing.T) {
	e, _, url := startFeed(t, Config{Interval: 5 * time.Millisecond})
	c, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()

	if first := read(t, c); first.Seq != "0" || len(first.Bids) != 0 {
		t.Fatalf("first message = %+v, want the empty book at seq 0", first)
	}
	submit(t, e, 99, "fund", command.Fund{To: 1, Asset: domain.USDT, Amount: 10_000 * domain.AtomsPerUSDT})
	placed := submit(t, e, 1, "bid", command.PlaceOrder{Side: domain.Buy, Price: 60_000, Qty: 1_000, TIF: domain.GTC})

	want := fmt.Sprint(placed.Seq)
	for {
		m := read(t, c)
		if m.Seq != want {
			continue // an intermediate snapshot (after the funding)
		}
		if len(m.Bids) != 1 || m.Bids[0].Price != "60000" || m.Bids[0].Quantity != "0.1000" {
			t.Fatalf("snapshot at seq %s = %+v, want one bid 0.1000 @ 60000", want, m)
		}
		return
	}
}

// TestSlowBrowserNeverBlocksMatching is the week-6 market-data gate. One
// browser stops reading completely; another reads normally. Orders keep
// flowing through the engine the whole time. Matching latency must stay
// low, the stuck browser must eventually be cut off, and the healthy one
// must keep receiving updates.
func TestSlowBrowserNeverBlocksMatching(t *testing.T) {
	e, h, url := startFeed(t, Config{Interval: time.Millisecond, Buffer: 2, WriteTimeout: 200 * time.Millisecond})

	stuck, _, err := websocket.Dial(context.Background(), url, nil) // never reads again
	if err != nil {
		t.Fatal(err)
	}
	defer stuck.CloseNow()
	healthy, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer healthy.CloseNow()
	var received atomic.Int64
	go func() {
		for {
			if _, _, err := healthy.Read(context.Background()); err != nil {
				return
			}
			received.Add(1)
		}
	}()

	// Big snapshots fill the stuck browser's socket buffers quickly: 20
	// resting levels on each side, plus a trade tape.
	submit(t, e, 99, "f1", command.Fund{To: 1, Asset: domain.USDT, Amount: 1_000_000 * domain.AtomsPerUSDT}) // 1 million USDT
	submit(t, e, 99, "f2", command.Fund{To: 2, Asset: domain.BTC, Amount: 1_000 * domain.LotsPerBTC})
	for i := range 20 {
		submit(t, e, 1, fmt.Sprint("b", i), command.PlaceOrder{Side: domain.Buy, Price: domain.Price(50_000 - i), Qty: 1_000, TIF: domain.GTC})
		submit(t, e, 2, fmt.Sprint("a", i), command.PlaceOrder{Side: domain.Sell, Price: domain.Price(70_000 + i), Qty: 1_000, TIF: domain.GTC})
	}

	var worst time.Duration
	deadline := time.Now().Add(20 * time.Second)
	for i := 0; h.Subscribers() > 1; i++ {
		if time.Now().After(deadline) {
			t.Fatal("the stuck browser was never disconnected")
		}
		start := time.Now()
		// A trade on every iteration, so every snapshot differs.
		submit(t, e, 1, fmt.Sprint("t", i), command.PlaceOrder{Side: domain.Buy, Price: 70_000, Qty: 1, TIF: domain.IOC})
		worst = max(worst, time.Since(start))
	}
	if worst > 500*time.Millisecond {
		t.Errorf("slowest order took %s while a browser was stuck: matching waited for it", worst)
	}
	before := received.Load()
	submit(t, e, 1, "after", command.PlaceOrder{Side: domain.Buy, Price: 70_000, Qty: 1, TIF: domain.IOC})
	time.Sleep(100 * time.Millisecond)
	if received.Load() <= before {
		t.Error("the healthy browser stopped receiving after the stuck one was dropped")
	}
	t.Logf("stuck browser dropped; slowest order %s; healthy browser received %d messages", worst, received.Load())
}

// TestShutdownClosesStreamsWithAReason: the client is told why it was
// disconnected, so a UI can show "reconnecting" instead of a silent freeze.
func TestShutdownClosesStreamsWithAReason(t *testing.T) {
	e := engine.New(state.New(), engine.DefaultConfig())
	go e.Run()
	defer func() { e.Close(); <-e.Done() }()
	h := New(e.Snapshot, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()

	c, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	read(t, c) // the initial snapshot
	cancel()

	rctx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer rcancel()
	_, _, err = c.Read(rctx)
	if websocket.CloseStatus(err) != websocket.StatusTryAgainLater || !strings.Contains(err.Error(), ReasonShutdown) {
		t.Fatalf("read after shutdown: %v; want close 1013 with %q", err, ReasonShutdown)
	}
}
