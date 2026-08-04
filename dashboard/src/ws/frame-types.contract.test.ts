import { describe, expect, it } from "vitest";

import backendFrameTypes from "../../../api/internal/ws/frame_types.json";

import { KNOWN_FRAME_TYPES } from "./scoreboard-socket";

// Cross-language contract (4a-ii): the set of WebSocket frame types the dashboard
// knows about must equal the backend's authoritative set. The backend generates
// api/internal/ws/frame_types.json from its FrameTypes constants (pinned by a Go
// test); this asserts the client's KNOWN_FRAME_TYPES matches it, so a frame added
// to the backend without teaching the client about it fails here rather than
// being silently dropped at runtime.
describe("WebSocket frame-type contract", () => {
  it("KNOWN_FRAME_TYPES equals the backend's frame_types.json", () => {
    expect([...KNOWN_FRAME_TYPES].sort()).toEqual([...backendFrameTypes].sort());
  });
});
