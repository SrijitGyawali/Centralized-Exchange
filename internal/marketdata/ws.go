package marketdata

import (
	"context"
	"errors"
	"net/http"

	"github.com/coder/websocket"
)

// Handler serves the public market-data stream over WebSocket
// (GET /stream/book).
//
// Each connection gets its own Subscriber and runs this loop in its own
// goroutine (net/http starts one per request):
//
//	message from the hub ──► write it (bounded by WriteTimeout)
//	hub dropped us        ──► close with the reason (slow consumer / shutdown)
//	client went away      ──► stop
//
// The stream is public and read-only: no token, no private data (fills
// carry no users or order IDs). The library's default origin check only
// accepts pages served from this same host, so another website cannot
// open the stream from a visitor's browser.
func (h *Hub) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return // Accept has already answered with an HTTP error
		}
		defer c.CloseNow()

		sub, err := h.Subscribe()
		if err != nil {
			reason := "too many subscribers, try again later"
			if errors.Is(err, ErrClosed) {
				reason = ReasonShutdown
			}
			c.Close(websocket.StatusTryAgainLater, reason)
			return
		}
		defer h.Unsubscribe(sub)

		// The feed is one-way. CloseRead reads (and discards) anything the
		// client sends, answers pings, and cancels ctx when the client
		// closes the connection.
		ctx := c.CloseRead(r.Context())
		for {
			select {
			case msg := <-sub.C():
				wctx, cancel := context.WithTimeout(ctx, h.cfg.WriteTimeout)
				err := c.Write(wctx, websocket.MessageText, msg)
				cancel()
				if err != nil {
					return // client gone or stuck past WriteTimeout
				}
			case <-sub.Done():
				c.Close(websocket.StatusTryAgainLater, sub.Reason())
				return
			case <-ctx.Done():
				return
			}
		}
	})
}
