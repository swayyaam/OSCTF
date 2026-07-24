// Package ws is the WebSocket hub for live scoreboard updates: a connection
// registry, throttled latest-wins broadcast, ping/pong keepalive, and graceful
// drain on shutdown. It imports scoreboard for the snapshot type only.
package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/coder/websocket"

	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/scoreboard"
)

// message is the envelope sent to clients: {"type": ..., "data": ...}.
type message struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

const (
	sendBuffer      = 8
	broadcastMinGap = time.Second
	pingInterval    = 30 * time.Second
	writeTimeout    = 10 * time.Second
)

type client struct {
	conn *websocket.Conn
	send chan []byte
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

	// Read/written only inside Run (no cross-goroutine access → no races).
	lastScoreboard []byte
	lastFrozen     bool
}

// NewHub builds a hub. Call Run in a goroutine to start it.
func NewHub(log *slog.Logger) *Hub {
	return &Hub{
		log:        log,
		register:   make(chan *client),
		unregister: make(chan *client),
		incoming:   make(chan outbound, 16),
		clients:    make(map[*client]struct{}),
		done:       make(chan struct{}),
	}
}

// Run is the hub's single-goroutine event loop. It returns when ctx is cancelled,
// closing all connections with code 1001 (going away).
func (h *Hub) Run(ctx context.Context) {
	var (
		lastSend time.Time
		pending  []byte
		havePend bool
		timer    = time.NewTimer(time.Hour)
	)
	timer.Stop()
	flush := func() {
		if !havePend {
			return
		}
		h.fanout(pending)
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
			// Greet with the frozen flag and the last known scoreboard.
			c.enqueue(encode("hello", map[string]bool{"frozen": h.lastFrozen}))
			if h.lastScoreboard != nil {
				c.enqueue(h.lastScoreboard)
			}

		case c := <-h.unregister:
			if _, ok := h.clients[c]; ok {
				delete(h.clients, c)
				close(c.send)
				metrics.WSConnections.Set(float64(len(h.clients)))
			}

		case msg := <-h.incoming:
			if msg.isScoreboard {
				h.lastScoreboard = msg.data
				h.lastFrozen = msg.frozen
			}
			pending = msg.data
			havePend = true
			if since := time.Since(lastSend); since >= broadcastMinGap {
				flush()
			} else {
				timer.Reset(broadcastMinGap - since)
			}

		case <-timer.C:
			flush()
		}
	}
}

// fanout writes msg to every client's send queue, dropping messages for clients
// whose queue is full (a slow consumer must not stall the hub).
func (h *Hub) fanout(msg []byte) {
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
			// Queue full: drop this update for the slow client; the next one wins.
		}
	}
}

// BroadcastScoreboard queues a scoreboard snapshot for delivery (throttled).
func (h *Hub) BroadcastScoreboard(snap scoreboard.Snapshot) {
	b, err := json.Marshal(message{Type: "scoreboard", Data: snap})
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
	b, err := json.Marshal(message{Type: "event.phase", Data: map[string]string{"phase": phase}})
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

func (c *client) enqueue(msg []byte) {
	select {
	case c.send <- msg:
	default:
	}
}
