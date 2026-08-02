package ws

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/osctf/platform/internal/apigen"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

type frame struct {
	typ  string
	data map[string]any
}

// drainWhenReady polls a client's ordered queue until at least n frames have accumulated
// (the hub enqueues from its own goroutine), preserving arrival order across polls.
func drainWhenReady(t *testing.T, c *client, n int) []frame {
	t.Helper()
	var got []frame
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		frames, _ := c.drain()
		for _, f := range frames {
			typ, data := parseMsg(t, f)
			got = append(got, frame{typ: typ, data: data})
		}
		if len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d frames; got %d", n, len(got))
	return nil
}

// parseMsg decodes an outbound frame into its envelope + a data field lookup.
func parseMsg(t *testing.T, b []byte) (typ string, data map[string]any) {
	t.Helper()
	var m struct {
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal frame %q: %v", b, err)
	}
	return m.Type, m.Data
}

func TestEncodeProducesTypedEnvelope(t *testing.T) {
	typ, data := parseMsg(t, encode("hello", map[string]bool{"frozen": true}))
	if typ != "hello" {
		t.Errorf("type = %q, want hello", typ)
	}
	if data["frozen"] != true {
		t.Errorf("data.frozen = %v, want true", data["frozen"])
	}
}

func TestBroadcastScoreboardEnqueuesEnvelope(t *testing.T) {
	h := NewHub(discardLogger())
	h.BroadcastScoreboard(apigen.Scoreboard{Frozen: true, Standings: []apigen.ScoreboardEntry{}})
	select {
	case msg := <-h.incoming:
		if !msg.isScoreboard {
			t.Error("scoreboard message not marked isScoreboard (won't be cached for replay)")
		}
		if !msg.frozen {
			t.Error("frozen flag not propagated to the outbound")
		}
		if typ, _ := parseMsg(t, msg.data); typ != "scoreboard" {
			t.Errorf("envelope type = %q, want scoreboard", typ)
		}
	default:
		t.Fatal("BroadcastScoreboard enqueued nothing")
	}
}

func TestBroadcastPhaseEnqueuesEnvelope(t *testing.T) {
	h := NewHub(discardLogger())
	h.BroadcastPhase("ended")
	select {
	case msg := <-h.incoming:
		if msg.isScoreboard {
			t.Error("phase message wrongly marked isScoreboard")
		}
		typ, data := parseMsg(t, msg.data)
		if typ != "event.phase" || data["phase"] != "ended" {
			t.Errorf("phase envelope = %q/%v, want event.phase/ended", typ, data["phase"])
		}
	default:
		t.Fatal("BroadcastPhase enqueued nothing")
	}
}

// TestBroadcastNeverBlocksWhenIncomingFull: a saturated incoming queue must drop
// the newest broadcast, not block the caller (a scoreboard recompute or phase tick).
func TestBroadcastNeverBlocksWhenIncomingFull(t *testing.T) {
	h := NewHub(discardLogger())
	for i := 0; i < cap(h.incoming); i++ {
		h.incoming <- outbound{}
	}
	done := make(chan struct{})
	go func() {
		h.BroadcastPhase("ended")
		h.BroadcastScoreboard(apigen.Scoreboard{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Broadcast blocked on a full incoming queue; must drop instead")
	}
}

// TestClientQueuePreservesOrderAndCoalescesSnapshots: frames leave in arrival order
// (hello first; a phase frame and the snapshots around it never reordered), and
// CONSECUTIVE snapshots coalesce to the latest — but a snapshot after a phase does not
// coalesce back across it.
func TestClientQueuePreservesOrderAndCoalescesSnapshots(t *testing.T) {
	c := newClient(nil, "")
	c.enqueue(encode("hello", map[string]bool{"frozen": false}), false)
	c.enqueue(encode("scoreboard", map[string]int{"v": 1}), true)
	c.enqueue(encode("scoreboard", map[string]int{"v": 2}), true) // coalesces with v:1
	c.enqueue(encode("event.phase", map[string]string{"phase": "ended"}), false)
	c.enqueue(encode("scoreboard", map[string]int{"v": 3}), true) // after phase → not coalesced

	frames, overflow := c.drain()
	if overflow {
		t.Fatal("unexpected overflow")
	}
	var types []string
	for _, f := range frames {
		typ, _ := parseMsg(t, f)
		types = append(types, typ)
	}
	want := []string{"hello", "scoreboard", "event.phase", "scoreboard"}
	if len(types) != len(want) {
		t.Fatalf("frame types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("frame types = %v, want %v (hello first; phase not reordered vs snapshots)", types, want)
		}
	}
	if _, d := parseMsg(t, frames[1]); d["v"] != float64(2) {
		t.Errorf("coalesced snapshot v = %v, want 2 (latest-wins to the tail)", d["v"])
	}
	if _, d := parseMsg(t, frames[3]); d["v"] != float64(3) {
		t.Errorf("post-phase snapshot v = %v, want 3", d["v"])
	}
}

// TestClientQueueOverflowMarksDisconnect: a backlog past the cap marks the client for
// disconnect rather than silently dropping frames (a missed phase would be permanent).
func TestClientQueueOverflowMarksDisconnect(t *testing.T) {
	c := newClient(nil, "")
	for i := 0; i < clientQueueCap+5; i++ {
		c.enqueue(encode("event.phase", map[string]string{"phase": "x"}), false)
	}
	if _, overflow := c.drain(); !overflow {
		t.Fatal("backlog past clientQueueCap did not mark the client for disconnect")
	}
}

// TestRunGreetsAndReplaysScoreboard: a newly registered client is greeted with the
// current frozen flag, and once a scoreboard has been broadcast the hub replays the
// latest snapshot to every subsequent joiner.
func TestRunGreetsAndReplaysScoreboard(t *testing.T) {
	h := NewHub(discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)

	c1 := newClient(nil, "")
	h.register <- c1
	if frames := drainWhenReady(t, c1, 1); frames[0].typ != "hello" || frames[0].data["frozen"] != false {
		t.Fatalf("first greet = %q frozen=%v, want hello frozen=false", frames[0].typ, frames[0].data["frozen"])
	}

	// Broadcast a frozen scoreboard; c1 receives it (first broadcast flushes at once) and
	// the hub caches it as the latest.
	h.BroadcastScoreboard(apigen.Scoreboard{Frozen: true, Standings: []apigen.ScoreboardEntry{}})
	if frames := drainWhenReady(t, c1, 1); frames[0].typ != "scoreboard" {
		t.Fatalf("c1 broadcast = %q, want scoreboard", frames[0].typ)
	}

	// A late joiner is greeted with frozen=true (from the cached snapshot) and then the
	// replayed scoreboard — in that order, in the one ordered queue.
	c2 := newClient(nil, "")
	h.register <- c2
	frames := drainWhenReady(t, c2, 2)
	if frames[0].typ != "hello" || frames[0].data["frozen"] != true {
		t.Fatalf("late-joiner greet = %q frozen=%v, want hello frozen=true", frames[0].typ, frames[0].data["frozen"])
	}
	if frames[1].typ != "scoreboard" {
		t.Fatalf("late-joiner second frame = %q, want scoreboard (hello must precede the board)", frames[1].typ)
	}

	// Unregister both before cancelling so Run's shutdown path has no client conns
	// to Close (these fakes have a nil *websocket.Conn).
	h.unregister <- c1
	h.unregister <- c2
	cancel()
	<-h.done
}
