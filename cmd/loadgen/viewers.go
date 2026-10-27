package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// slowViewers simulates browsers on a bad connection: each opens the public
// book stream, then reads only one message every readEvery. The hub sends
// up to 10 messages a second, so a viewer like this fills its buffer and is
// disconnected ("slow consumer"); it then reconnects, as the web UI does.
//
// Running these during a load test checks the week-6 promise under load: a
// slow browser costs the engine nothing. Their drops appear in the report
// next to the trading latencies of the same stage.
type slowViewers struct {
	connects atomic.Int64 // successful connections, including reconnects
	drops    atomic.Int64 // disconnects observed by the viewers
	wg       sync.WaitGroup
}

func startSlowViewers(ctx context.Context, n int, url string, readEvery time.Duration) *slowViewers {
	v := &slowViewers{}
	for range n {
		v.wg.Add(1)
		go func() {
			defer v.wg.Done()
			for ctx.Err() == nil {
				c, _, err := websocket.Dial(ctx, url, nil)
				if err != nil {
					sleepCtx(ctx, time.Second)
					continue
				}
				v.connects.Add(1)
				for ctx.Err() == nil {
					if _, _, err := c.Read(ctx); err != nil {
						if ctx.Err() == nil {
							v.drops.Add(1)
						}
						break
					}
					sleepCtx(ctx, readEvery) // the slowness: messages pile up meanwhile
				}
				c.CloseNow()
			}
		}()
	}
	return v
}

// streamURL turns the API base URL into the stream's WebSocket URL:
// http://host:8080 -> ws://host:8080/stream/book.
func streamURL(base string) string {
	base = strings.TrimRight(base, "/")
	if rest, ok := strings.CutPrefix(base, "https://"); ok {
		return "wss://" + rest + "/stream/book"
	}
	return "ws://" + strings.TrimPrefix(base, "http://") + "/stream/book"
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
