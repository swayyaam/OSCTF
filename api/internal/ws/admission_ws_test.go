package ws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/osctf/platform/internal/apigen"
)

// TestHandlerRejectsOverCapCleanly (3a-x): once the global connection cap is reached, a
// further handshake is refused with a plain 429 BEFORE the upgrade — no connection is
// allocated — and the connections already established keep receiving broadcasts. A
// regression that dropped the cap, or that degraded the hub when rejecting, fails here.
func TestHandlerRejectsOverCapCleanly(t *testing.T) {
	hub := NewHub(discardLogger())
	hub.SetLimits(Limits{MaxConns: 2, MaxConnsPerKey: 100, HandshakeBurst: 100, HandshakeWindow: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	ts := httptest.NewServer(hub.Handler())
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")

	dial := func() (*websocket.Conn, *http.Response, error) {
		dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer dcancel()
		return websocket.Dial(dctx, url, nil) //nolint:bodyclose // library manages the body
	}

	c1, _, err := dial()
	if err != nil {
		t.Fatalf("first connection rejected: %v", err)
	}
	defer c1.Close(websocket.StatusNormalClosure, "")
	if typ := readType(t, c1); typ != "hello" {
		t.Fatalf("c1 first frame = %q, want hello", typ)
	}
	c2, _, err := dial()
	if err != nil {
		t.Fatalf("second connection rejected: %v", err)
	}
	defer c2.Close(websocket.StatusNormalClosure, "")

	// Third handshake exceeds the cap: a clean 429, no upgrade.
	c3, resp, err := dial()
	if err == nil {
		_ = c3.Close(websocket.StatusNormalClosure, "")
		t.Fatal("third dial succeeded past the global connection cap")
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("rejected handshake status = %v, want 429", resp)
	}

	// A rejected handshake carries Retry-After so proxies and non-browser clients back off.
	if ra := resp.Header.Get("Retry-After"); ra == "" {
		t.Error("rejected handshake missing Retry-After header")
	}

	// The rejection did not degrade the hub: c1 still receives a live broadcast.
	hub.BroadcastScoreboard(apigen.Scoreboard{Frozen: false, Standings: []apigen.ScoreboardEntry{}})
	if typ := readType(t, c1); typ != "scoreboard" {
		t.Fatalf("existing client c1 frame after a rejected handshake = %q, want scoreboard", typ)
	}
}

// TestHandlerReleasesSlotOnAbruptDrop (3a-xi-2): a client that vanishes with no close
// frame (wifi drop) must free its admission slot, or dead slots pile up against the
// per-key cap and legitimate reconnects are rejected while the hub holds no real clients.
// Fill the per-key cap with abruptly-dropped clients and assert a new connection is
// accepted once they are reaped.
func TestHandlerReleasesSlotOnAbruptDrop(t *testing.T) {
	hub := NewHub(discardLogger())
	hub.SetLimits(Limits{MaxConns: 100, MaxConnsPerKey: 3, HandshakeBurst: 10000, HandshakeWindow: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx)

	ts := httptest.NewServer(hub.Handler())
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http")

	dial := func() (*websocket.Conn, *http.Response, error) {
		dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer dcancel()
		return websocket.Dial(dctx, url, nil) //nolint:bodyclose // library manages the body
	}

	// Fill the per-key cap (all dials share the 127.0.0.1 key), then drop abruptly.
	var conns []*websocket.Conn
	for i := 0; i < 3; i++ {
		c, _, err := dial()
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		if typ := readType(t, c); typ != "hello" {
			t.Fatalf("dial %d first frame = %q, want hello", i, typ)
		}
		conns = append(conns, c)
	}
	if c, resp, err := dial(); err == nil {
		_ = c.Close(websocket.StatusNormalClosure, "")
		t.Fatal("4th dial accepted while the per-key cap was full")
	} else if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over-cap dial status = %v, want 429", resp)
	}

	for _, c := range conns {
		_ = c.CloseNow() // vanish with no close handshake
	}
	// Nudge the hub so the write pumps notice the dead sockets and tear down.
	hub.BroadcastScoreboard(apigen.Scoreboard{Standings: []apigen.ScoreboardEntry{}})

	// The slots must free: a fresh connection from the same key is eventually accepted.
	accepted := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, _, err := dial(); err == nil {
			_ = c.Close(websocket.StatusNormalClosure, "")
			accepted = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !accepted {
		t.Fatalf("per-key cap never freed after abrupt drops (live total=%d) — dead slots accumulated", hub.admit.liveTotal())
	}
}

// readType reads one frame from conn and returns its envelope type.
func readType(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	rctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := conn.Read(rctx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	typ, _ := parseMsg(t, data)
	return typ
}
