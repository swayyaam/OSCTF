package ws

// WebSocket frame golden tests. 3a-xiii found a WS/REST field-order divergence
// in the scoreboard payload; these pin the wire shape of every frame the hub
// emits and assert the scoreboard frame's data is byte-identical to the REST
// serialization of the same apigen.Scoreboard (the one-wire-type guarantee).
//
// encode(typ, data) is the exact serializer BroadcastScoreboard / BroadcastPhase
// and the hello greeting all use (json.Marshal(message{Type, Data})), so pinning
// its output pins what a client actually receives. Regenerate with UPDATE_GOLDEN=1.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/osctf/platform/internal/apigen"
)

func wsGolden(t *testing.T, name string, raw []byte) {
	t.Helper()
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err != nil {
		t.Fatalf("indent %s: %v", name, err)
	}
	got := append(indented.Bytes(), '\n')
	path := filepath.Join("testdata", name+".json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (UPDATE_GOLDEN=1 to create): %v", path, err)
	}
	if !bytes.Equal(bytes.TrimRight(want, "\n"), bytes.TrimRight(got, "\n")) {
		t.Errorf("ws frame %s mismatch (UPDATE_GOLDEN=1 to accept):\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

func TestWSFrameGolden(t *testing.T) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fullBoard := apigen.Scoreboard{
		Frozen:      false,
		GeneratedAt: fixed,
		Standings: []apigen.ScoreboardEntry{{
			Rank: intp(1), TeamId: fixedUUID(2), Name: "Team Alpha", Points: 500,
			LastSolveAt: &fixed, Solves: 3, Banned: false,
		}},
	}
	emptyBoard := apigen.Scoreboard{Frozen: false, GeneratedAt: fixed, Standings: []apigen.ScoreboardEntry{}}

	// hello: {"type":"hello","data":{"frozen":false}} — the exact greeting the hub
	// enqueues before any board.
	wsGolden(t, "frame_hello", encode("hello", map[string]bool{"frozen": false}))

	// event.phase: never coalesced/dropped; carries the current phase string.
	wsGolden(t, "frame_event_phase", encode("event.phase", map[string]string{"phase": "running"}))

	// scoreboard frames: empty board's standings must be [] (a nil slice → null
	// would throw at render in the dashboard, which reads standings.length/.map
	// unguarded), and the full shape is pinned.
	wsGolden(t, "frame_scoreboard_empty", encode("scoreboard", emptyBoard))
	wsGolden(t, "frame_scoreboard_full", encode("scoreboard", fullBoard))
}

// TestWSScoreboardMatchesREST is the 3a-xiii guarantee: the scoreboard frame's
// data field is byte-identical to the standalone (REST) serialization of the
// same apigen.Scoreboard — one wire type, so WS and REST cannot diverge in
// field name, order, or null-vs-[].
func TestWSScoreboardMatchesREST(t *testing.T) {
	for _, board := range []apigen.Scoreboard{
		{Frozen: true, GeneratedAt: time.Unix(0, 0).UTC(), Standings: []apigen.ScoreboardEntry{}},
		{Frozen: false, GeneratedAt: time.Unix(0, 0).UTC(), Standings: []apigen.ScoreboardEntry{{Rank: intp(1), TeamId: fixedUUID(2), Name: "A", Points: 1, Solves: 1}}},
	} {
		frame := encode("scoreboard", board)
		var env struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(frame, &env); err != nil {
			t.Fatalf("unmarshal frame: %v", err)
		}
		if env.Type != "scoreboard" {
			t.Errorf("frame type = %q, want scoreboard", env.Type)
		}
		restBytes, err := json.Marshal(board)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(env.Data, restBytes) {
			t.Errorf("WS scoreboard data diverges from REST serialization:\n WS:   %s\n REST: %s", env.Data, restBytes)
		}
	}
}

func intp(i int) *int { return &i }

func fixedUUID(n byte) openapi_types.UUID {
	var u openapi_types.UUID
	for i := range u {
		u[i] = n
	}
	return u
}
