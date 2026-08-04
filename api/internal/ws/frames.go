package ws

// Frame types emitted on the scoreboard WebSocket. This is the single source of
// truth for the wire protocol's frame names — the hub references these constants
// instead of string literals, and frame_types.json is generated from FrameTypes.
//
// The dashboard's ScoreboardSocket must know exactly this set: a frame added here
// that the client does not branch on is parsed and silently dropped. A contract
// test on each side asserts its known set against frame_types.json, so adding a
// backend frame without teaching the dashboard about it fails CI (4a-ii).
const (
	FrameHello      = "hello"       // greeting; carries the current frozen flag
	FrameScoreboard = "scoreboard"  // a scoreboard snapshot (apigen.Scoreboard)
	FrameEventPhase = "event.phase" // an event phase transition
)

// FrameTypes is the authoritative set of frame names, sorted for a stable
// contract file. Keep frame_types.json in sync (the ws contract test rewrites it
// with UPDATE_GOLDEN=1).
var FrameTypes = []string{FrameEventPhase, FrameHello, FrameScoreboard}
