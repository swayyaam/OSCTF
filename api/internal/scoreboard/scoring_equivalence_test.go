package scoreboard

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/db/gen"
)

// TestScoringRegistryStandingsEquivalence pins that the registry-backed scoring (with only
// the built-ins registered) produces BYTE-IDENTICAL standings to v0.2.2 for a fixed
// static+dynamic submission log. The scoreboard is where the scoring-related fixes
// clustered and where the soak's "board is recomputable from the log" invariant lives, so
// an equivalence check here is worth more than the compile-level gate: any drift in what
// scoring.Value returns for the built-ins would move these bytes.
func TestScoringRegistryStandingsEquivalence(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return now.Add(time.Duration(-h) * time.Hour) }

	teamA := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	teamB := uuid.MustParse("00000000-0000-0000-0000-0000000000b2")
	teamC := uuid.MustParse("00000000-0000-0000-0000-0000000000c3")
	teamZ := uuid.MustParse("00000000-0000-0000-0000-0000000000f9") // zero solves
	chS := uuid.MustParse("00000000-0000-0000-0000-00000000000a")   // static 100
	chD := uuid.MustParse("00000000-0000-0000-0000-00000000000b")   // dynamic 500/100/10

	stat := func(team uuid.UUID, at time.Time) gen.ListValidSolvesRow {
		return gen.ListValidSolvesRow{TeamID: team, ChallengeID: chS, SolvedAt: at, Scoring: "static", PointsInitial: 100}
	}
	dyn := func(team uuid.UUID, at time.Time) gen.ListValidSolvesRow {
		return gen.ListValidSolvesRow{TeamID: team, ChallengeID: chD, SolvedAt: at, Scoring: "dynamic", PointsInitial: 500, PointsMin: i32p(100), Decay: i32p(10)}
	}
	store := fakeStore{
		teams: []gen.ListScoreboardTeamsRow{
			{ID: teamA, Name: "Alpha"},
			{ID: teamB, Name: "Bravo"},
			{ID: teamC, Name: "Charlie"},
			{ID: teamZ, Name: "Zeta"},
		},
		solves: []gen.ListValidSolvesRow{
			stat(teamA, at(5)), stat(teamB, at(4)), stat(teamC, at(3)),
			dyn(teamA, at(2)), dyn(teamB, at(1)), // chD total solves = 2 → dynamic value 484
		},
	}

	snap, err := compute(context.Background(), store, now)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	got, err := json.MarshalIndent(snap.Standings, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Golden = the v0.2.2 standings for this log. Regenerating it requires a deliberate
	// scoring change (which would be a v1.0 API/scoring decision), not a silent refactor.
	const want = `[
  {
    "banned": false,
    "last_solve_at": "2026-06-01T10:00:00Z",
    "name": "Alpha",
    "points": 584,
    "rank": 1,
    "solves": 2,
    "team_id": "00000000-0000-0000-0000-0000000000a1"
  },
  {
    "banned": false,
    "last_solve_at": "2026-06-01T11:00:00Z",
    "name": "Bravo",
    "points": 584,
    "rank": 2,
    "solves": 2,
    "team_id": "00000000-0000-0000-0000-0000000000b2"
  },
  {
    "banned": false,
    "last_solve_at": "2026-06-01T09:00:00Z",
    "name": "Charlie",
    "points": 100,
    "rank": 3,
    "solves": 1,
    "team_id": "00000000-0000-0000-0000-0000000000c3"
  },
  {
    "banned": false,
    "last_solve_at": null,
    "name": "Zeta",
    "points": 0,
    "rank": 4,
    "solves": 0,
    "team_id": "00000000-0000-0000-0000-0000000000f9"
  }
]`
	if string(got) != want {
		t.Fatalf("standings not byte-identical to v0.2.2:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
