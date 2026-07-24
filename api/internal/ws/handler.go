package ws

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// Handler upgrades the request to a WebSocket, registers the connection, and
// pumps broadcasts until the client disconnects or the server shuts down.
// v0.1 has no per-connection auth; the endpoint is public.
func (h *Hub) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// Same-origin in production; the browser enforces origin for WS.
			InsecureSkipVerify: true,
		})
		if err != nil {
			return // Accept already wrote the error
		}
		c := &client{conn: conn, send: make(chan []byte, sendBuffer)}
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
// frames (ping/pong/close); any read error tears the connection down.
func (h *Hub) readPump(ctx context.Context, c *client, cancel context.CancelFunc) {
	defer cancel()
	for {
		if _, _, err := c.conn.Read(ctx); err != nil {
			return
		}
	}
}

// writePump delivers queued broadcasts and sends periodic pings. It returns when
// the send channel closes (unregister), a write fails, or the context is done.
func (h *Hub) writePump(ctx context.Context, c *client) {
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-c.send:
			if !ok {
				return
			}
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := c.conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return
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
