// Package ws is the WebSocket hub for live scoreboard updates: a connection
// registry, throttled latest-wins broadcast, ping/pong keepalive, and graceful
// drain on shutdown. It broadcasts apigen.Scoreboard — the SAME wire type the REST
// endpoint returns — so a client that reconciles WS pushes against a REST fetch sees
// byte-identical payloads by construction.
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/metrics"
)

// message is the envelope sent to clients: {"type": ..., "data": ...}.
type message struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

const (
	broadcastMinGap = time.Second
	pingInterval    = 30 * time.Second
	writeTimeout    = 10 * time.Second
	// clientQueueCap bounds a slow client's ordered backlog. Snapshots coalesce, so the
	// backlog only grows with non-coalescable frames (hello, event.phase) a client cannot
	// keep up with; past the cap the client is disconnected (it reconnects with a fresh
	// greeting) rather than have frames silently dropped.
	clientQueueCap = 64
)

// client holds a connection's single ORDERED outbound queue. Frames are delivered in the
// order enqueued — hello before any snapshot, and a phase frame and the snapshot for that
// phase never reordered — so a client can't render a post-freeze board as live. Snapshots
// are still coalesced, but only against the TAIL: a new snapshot replaces the pending one
// ONLY when no ordered frame (hello/phase) was enqueued after it, so a slow client on a
// large board holds one snapshot per phase interval, not a backlog of stale boards.
//
// CORRECTNESS DEPENDENCY: event.phase frames are never dropped or coalesced, so a client
// always converges on the true phase — BUT that guarantee holds only while the queue does
// not overflow. Past clientQueueCap the recovery path is to DISCONNECT the client
// (writePump, StatusPolicyViolation) so it reconnects and is re-greeted with the current
// state; the reconnect handshake is exempted (admissions.forgiveHandshake) so a shed
// client is never locked out. If you ever make phase delivery depend on something other
// than "stays queued or reconnects", revisit this.
//
// The serialized snapshot []byte is shared across all clients (marshaled once per
// broadcast; the queue stores the slice header, not a copy), so the memory here is a
// slice header per pending frame, not a payload per client.
type client struct {
	conn     *websocket.Conn
	key      string // admission key, for forgiving the reconnect on an overflow disconnect
	mu       sync.Mutex
	queue    [][]byte // ordered pending frames; a shared []byte per entry
	tailSnap bool     // whether queue's last entry is a coalescable snapshot
	overflow bool     // backlog exceeded clientQueueCap → writePump disconnects
	wake     chan struct{}
}

func newClient(conn *websocket.Conn, key string) *client {
	return &client{conn: conn, key: key, wake: make(chan struct{}, 1)}
}

func (c *client) signal() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// enqueue appends a frame in arrival order. A coalescable snapshot replaces the tail ONLY
// when the tail is itself a snapshot (never across an intervening hello/phase), preserving
// order. A backlog past clientQueueCap marks the client for disconnect instead of dropping.
func (c *client) enqueue(msg []byte, isSnapshot bool) {
	c.mu.Lock()
	switch {
	case isSnapshot && c.tailSnap && len(c.queue) > 0:
		c.queue[len(c.queue)-1] = msg // coalesce with the tail snapshot (latest-wins)
	case len(c.queue) >= clientQueueCap:
		c.overflow = true
	default:
		c.queue = append(c.queue, msg)
		c.tailSnap = isSnapshot
	}
	c.mu.Unlock()
	c.signal()
}

// drain removes and returns all pending frames plus the overflow flag.
func (c *client) drain() (frames [][]byte, overflow bool) {
	c.mu.Lock()
	frames, c.queue, c.tailSnap, overflow = c.queue, nil, false, c.overflow
	c.mu.Unlock()
	return frames, overflow
}

// outbound is a message queued for broadcast. isScoreboard marks snapshots so the
// hub caches the latest for replay to new connections.
type outbound struct {
	data         []byte
	isScoreboard bool
	frozen       bool
}

// Hub owns the set of live connections and fans out broadcasts.
type Hub struct {
	log        *slog.Logger
	register   chan *client
	unregister chan *client
	incoming   chan outbound

	clients map[*client]struct{}
	done    chan struct{} // closed when Run exits

	// admit bounds the public endpoint (connection caps + handshake rate); see limits.go.
	admit *admissions

	// Read/written only inside Run (no cross-goroutine access → no races).
	lastScoreboard []byte
	lastFrozen     bool
}

// NewHub builds a hub with the default connection limits. Call Run in a goroutine to
// start it; use SetLimits/SetKeyResolver before serving to override the defaults.
func NewHub(log *slog.Logger) *Hub {
	return &Hub{
		log:        log,
		register:   make(chan *client),
		unregister: make(chan *client),
		incoming:   make(chan outbound, 16),
		clients:    make(map[*client]struct{}),
		done:       make(chan struct{}),
		admit:      newAdmissions(DefaultLimits()),
	}
}

// SetLimits overrides the connection caps and handshake rate limit. Call before serving.
func (h *Hub) SetLimits(l Limits) { h.admit.limits = l }

// SetKeyResolver installs the admission-key resolver: authenticated connections should
// key on the user id and anonymous ones on the (proxy-aware) client IP, so a shared NAT
// of logged-in players is not throttled as one IP. Call before serving. Nil (the default)
// keys on the socket peer IP.
func (h *Hub) SetKeyResolver(fn func(*http.Request) string) { h.admit.keyFn = fn }

// Run is the hub's single-goroutine event loop. It returns when ctx is cancelled,
// closing all connections with code 1001 (going away).
func (h *Hub) Run(ctx context.Context) {
	var (
		lastSend time.Time
		pending  []byte // a throttled, latest-wins SNAPSHOT awaiting flush (never a phase frame)
		havePend bool
		timer    = time.NewTimer(time.Hour)
	)
	timer.Stop()
	flush := func() {
		if !havePend {
			return
		}
		h.fanout(pending, true) // pending only ever holds a snapshot
		pending = nil
		havePend = false
		lastSend = time.Now()
	}

	defer close(h.done)
	for {
		select {
		case <-ctx.Done():
			for c := range h.clients {
				_ = c.conn.Close(websocket.StatusGoingAway, "server shutting down")
			}
			return

		case c := <-h.register:
			h.clients[c] = struct{}{}
			metrics.WSConnections.Set(float64(len(h.clients)))
			// Greet with the frozen flag, THEN the last known scoreboard — the ordered
			// queue guarantees hello reaches the client before any board.
			c.enqueue(encode(FrameHello, map[string]bool{"frozen": h.lastFrozen}), false)
			if h.lastScoreboard != nil {
				c.enqueue(h.lastScoreboard, true)
			}

		case c := <-h.unregister:
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				metrics.WSConnections.Set(float64(len(h.clients)))
			}

		case msg := <-h.incoming:
			if msg.isScoreboard {
				h.lastScoreboard = msg.data
				h.lastFrozen = msg.frozen
				pending = msg.data
				havePend = true
				if since := time.Since(lastSend); since >= broadcastMinGap {
					flush()
				} else {
					timer.Reset(broadcastMinGap - since)
				}
			} else {
				// event.phase: deliver promptly and in order. Flush any pending (older)
				// snapshot first so the phase never jumps ahead of an earlier board, and
				// never coalesce or drop it — a missed phase transition is permanent until
				// the client reconnects.
				flush()
				h.fanout(msg.data, false)
			}

		case <-timer.C:
			flush()
		}
	}
}

// fanout delivers msg to every client's ordered queue: snapshots coalesce against the
// tail (latest-wins), other frames append in order. A slow consumer never stalls the hub;
// one that falls too far behind is marked for disconnect by its own write pump.
func (h *Hub) fanout(msg []byte, isSnapshot bool) {
	for c := range h.clients {
		c.enqueue(msg, isSnapshot)
	}
}

// BroadcastScoreboard queues a scoreboard snapshot for delivery (throttled). It
// takes apigen.Scoreboard so the "data" it emits is byte-identical to the REST body.
func (h *Hub) BroadcastScoreboard(snap apigen.Scoreboard) {
	b, err := json.Marshal(message{Type: FrameScoreboard, Data: snap})
	if err != nil {
		h.log.Error("ws: marshaling scoreboard", "error", err.Error())
		return
	}
	select {
	case h.incoming <- outbound{data: b, isScoreboard: true, frozen: snap.Frozen}:
	default:
		// incoming is full; the latest snapshot will arrive on the next broadcast.
	}
}

// BroadcastPhase notifies clients of an event phase transition (not throttled;
// phase changes are rare and must arrive promptly).
func (h *Hub) BroadcastPhase(phase string) {
	b, err := json.Marshal(message{Type: FrameEventPhase, Data: map[string]string{"phase": phase}})
	if err != nil {
		return
	}
	select {
	case h.incoming <- outbound{data: b}:
	default:
	}
}

func encode(typ string, data any) []byte {
	b, _ := json.Marshal(message{Type: typ, Data: data})
	return b
}
