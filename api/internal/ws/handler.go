package ws

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/osctf/platform/internal/metrics"
)

// Handler upgrades the request to a WebSocket, registers the connection, and
// pumps broadcasts until the client disconnects or the server shuts down.
// The endpoint is public (no per-connection auth), so admission control — connection
// caps and a handshake rate limit — is what keeps an unauthenticated client from opening
// connections until the process dies. A rejected handshake returns 429 BEFORE the
// upgrade, so it never allocates a connection or touches the hub; existing clients are
// unaffected.
func (h *Hub) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := h.admit.keyOf(r)
		if limit, ok := h.admit.admit(key); !ok {
			metrics.WSRejections.WithLabelValues(limit).Inc()
			w.Header().Set("Retry-After", strconv.Itoa(h.admit.retryAfterSeconds(limit)))
			http.Error(w, "too many websocket connections", http.StatusTooManyRequests)
			return
		}
		released := false
		release := func() {
			if !released {
				released = true
				h.admit.release(key)
			}
		}
		defer release() // runs on clean close, abrupt drop, panic unwind, and shutdown

		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Same-origin in production; the browser enforces origin for WS.
			InsecureSkipVerify: true,
		})
		if err != nil {
			return // Accept already wrote the error; defer frees the slot
		}
		c := newClient(conn, key)
		select {
		case h.register <- c:
		case <-h.done:
			_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
			return
		}

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		go h.readPump(ctx, c, cancel)
		h.writePump(ctx, c)

		select {
		case h.unregister <- c:
		case <-h.done:
		}
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}
}

// readPump drains (and ignores) client messages so the library processes control
// frames (ping/pong/close); any read error tears the connection down. It runs in its own
// goroutine, so a panic here would otherwise escape net/http's per-request recover and
// crash the process; the recover funnels it back through cancel() → writePump returns →
// the handler returns → the admission slot is released. Slots therefore free on every
// teardown path, not only a graceful close.
func (h *Hub) readPump(ctx context.Context, c *client, cancel context.CancelFunc) {
	defer cancel()
	defer func() {
		if p := recover(); p != nil {
			metrics.WSReadPumpPanics.Inc() // a recovered panic must leave a trace, not vanish
			h.log.Error("ws: readPump panic recovered", "panic", p)
		}
	}()
	for {
		if _, _, err := c.conn.Read(ctx); err != nil {
			return
		}
	}
}

// writePump delivers the client's ordered frame queue and periodic pings. On each wake it
// drains the queue in order; a client whose backlog overflowed is disconnected (it
// reconnects with a fresh greeting) rather than served stale, out-of-order frames.
func (h *Hub) writePump(ctx context.Context, c *client) {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			frames, overflow := c.drain()
			if overflow {
				// Shed the slow client, but exempt its reconnect from the handshake rate
				// limit — the server chose to disconnect it, so it must not be locked out.
				h.admit.forgiveHandshake(c.key)
				_ = c.conn.Close(websocket.StatusPolicyViolation, "slow consumer")
				return
			}
			for _, f := range frames {
				if !h.writeFrame(ctx, c, f) {
					return
				}
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.conn.Ping(pctx)
			cancel()
			if err != nil && !errors.Is(err, context.Canceled) {
				return
			}
		}
	}
}

// writeFrame writes one message under the write timeout, reporting success.
func (h *Hub) writeFrame(ctx context.Context, c *client, msg []byte) bool {
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	err := c.conn.Write(wctx, websocket.MessageText, msg)
	cancel()
	return err == nil
}
