package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SrijitGyawali/Centralized-Exchange/internal/book"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/domain"
	"github.com/SrijitGyawali/Centralized-Exchange/internal/engine"
)

// fakeSource stands in for engine.Snapshot: the test decides what the
// "engine" shows, and calls tick() itself, so there are no timers.
type fakeSource struct {
	snap atomic.Pointer[engine.Snapshot]
}

func (f *fakeSource) set(seq domain.Seq) {
	f.snap.Store(&engine.Snapshot{
		Seq:    seq,
		Book:   book.Depth{Bids: []book.LevelSummary{{Price: 60_000, Qty: 1_000, Orders: 2}}},
		Trades: []engine.PublicTrade{{Seq: seq, Price: 60_100, Qty: 500, TakerSide: domain.Buy}},
	})
}

func newHub(t *testing.T, cfg Config) (*Hub, *fakeSource) {
	t.Helper()
	src := &fakeSource{}
	src.set(1)
	return New(src.snap.Load, cfg), src
}

func seqOf(t *testing.T, msg []byte) string {
	t.Helper()
	var m struct{ Seq string }
	if err := json.Unmarshal(msg, &m); err != nil {
		t.Fatal(err)
	}
	return m.Seq
}

func TestMessageFormat(t *testing.T) {
	src := &fakeSource{}
	src.set(42)
	got := string(encode(src.snap.Load()))
	want := `{"type":"snapshot","seq":"42","symbol":"BTC-USDT",` +
		`"bids":[{"price":"60000","quantity":"0.1000","orders":"2"}],"asks":[],` +
		`"trades":[{"seq":"42","price":"60100","quantity":"0.0500","side":"buy"}]}`
	if got != want {
		t.Errorf("message\n got  %s\n want %s", got, want)
	}
}

func TestNewSubscriberGetsTheCurrentBookAtOnce(t *testing.T) {
	h, _ := newHub(t, Config{})
	h.tick()
	s, err := h.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-s.C():
		if seqOf(t, msg) != "1" {
			t.Errorf("first message has seq %s, want 1", seqOf(t, msg))
		}
	default:
		t.Fatal("a new subscriber had to wait for the next change to see anything")
	}
}

func TestPublishesOnlyWhenSomethingChanged(t *testing.T) {
	h, src := newHub(t, Config{})
	h.tick()
	s, _ := h.Subscribe()
	<-s.C() // the initial snapshot

	h.tick() // same seq: silence
	h.tick()
	select {
	case msg := <-s.C():
		t.Fatalf("an unchanged market produced a message (seq %s)", seqOf(t, msg))
	default:
	}

	src.set(2)
	h.tick()
	if msg := <-s.C(); seqOf(t, msg) != "2" {
		t.Errorf("after a change: seq %s, want 2", seqOf(t, msg))
	}
}

// TestSlowSubscriberIsDroppedOthersUnaffected is the heart of the design: a
// subscriber that stops reading is disconnected; everyone else keeps
// receiving every update; and publishing never waits for anybody.
func TestSlowSubscriberIsDroppedOthersUnaffected(t *testing.T) {
	h, src := newHub(t, Config{Buffer: 4})
	h.tick()
	slow, _ := h.Subscribe() // never reads
	fast, _ := h.Subscribe()
	<-fast.C()

	start := time.Now()
	for seq := domain.Seq(2); seq <= 20; seq++ {
		src.set(seq)
		h.tick()
		if msg := <-fast.C(); seqOf(t, msg) != seqString(seq) {
			t.Fatalf("fast subscriber got seq %s, want %d", seqOf(t, msg), seq)
		}
	}
	if took := time.Since(start); took > time.Second {
		t.Errorf("publishing 19 updates took %s: something waited for the slow subscriber", took)
	}

	select {
	case <-slow.Done():
		if slow.Reason() != ReasonSlow {
			t.Errorf("reason = %q, want %q", slow.Reason(), ReasonSlow)
		}
	default:
		t.Fatal("the slow subscriber was never disconnected")
	}
	if n := h.Subscribers(); n != 1 {
		t.Errorf("%d subscribers remain, want only the fast one", n)
	}
}

func TestSubscriberLimit(t *testing.T) {
	h, _ := newHub(t, Config{MaxSubscribers: 2})
	for range 2 {
		if _, err := h.Subscribe(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.Subscribe(); !errors.Is(err, ErrTooManySubscribers) {
		t.Errorf("third subscriber: error = %v, want ErrTooManySubscribers", err)
	}
}

func TestShutdownDisconnectsEveryone(t *testing.T) {
	h, _ := newHub(t, Config{Interval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { h.Run(ctx); close(done) }()

	a, err := h.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	<-done
	select {
	case <-a.Done():
		if a.Reason() != ReasonShutdown {
			t.Errorf("reason = %q, want %q", a.Reason(), ReasonShutdown)
		}
	default:
		t.Fatal("shutdown left a subscriber connected")
	}
	if _, err := h.Subscribe(); !errors.Is(err, ErrClosed) {
		t.Errorf("Subscribe after shutdown: %v, want ErrClosed", err)
	}
}

func seqString(s domain.Seq) string { return seq(s) }
