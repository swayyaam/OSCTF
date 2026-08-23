package scoreboard

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/swayyaam/OSCTF/internal/db/gen"
)

// fakeStore feeds compute canned rows so the standings algorithm is exercised
// without a database.
type fakeStore struct {
	teams  []gen.ListScoreboardTeamsRow
	solves []gen.ListValidSolvesRow
}

func (f fakeStore) ListScoreboardTeams(context.Context) ([]gen.ListScoreboardTeamsRow, error) {
	return f.teams, nil
}
func (f fakeStore) ListValidSolves(context.Context) ([]gen.ListValidSolvesRow, error) {
	return f.solves, nil
}
func (f fakeStore) CountValidSolves(context.Context) (int64, error) {
	return int64(len(f.solves)), nil
}

func i32p(v int32) *int32 { return &v }

// TestComputeRanksTiebreaksBansAndDynamicValue drives one representative board
// through compute and checks every rule at once: points ranking, the
// last-solve-then-name tiebreak, zero-solve teams included, banned teams excluded
// from ranking and from the dynamic solve count, and dynamic value aggregation.
func TestComputeRanksTiebreaksBansAndDynamicValue(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	t1 := now.Add(-4 * time.Hour)
	t2 := now.Add(-3 * time.Hour)
	t3 := now.Add(-2 * time.Hour)
	t4 := now.Add(-1 * time.Hour)

	teamA := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	teamB := uuid.MustParse("00000000-0000-0000-0000-0000000000b2")
	teamC := uuid.MustParse("00000000-0000-0000-0000-0000000000c3")
	teamD := uuid.MustParse("00000000-0000-0000-0000-0000000000d4") // banned
	teamE := uuid.MustParse("00000000-0000-0000-0000-0000000000e5") // zero solves
	chX := uuid.MustParse("00000000-0000-0000-0000-00000000000a")   // static 100
	chY := uuid.MustParse("00000000-0000-0000-0000-00000000000b")   // dynamic 500/100/10

	staticX := func(team uuid.UUID, banned bool, at time.Time) gen.ListValidSolvesRow {
		return gen.ListValidSolvesRow{TeamID: team, ChallengeID: chX, SolvedAt: at, TeamBanned: banned, Scoring: "static", PointsInitial: 100}
	}
	dynY := func(team uuid.UUID, banned bool, at time.Time) gen.ListValidSolvesRow {
		return gen.ListValidSolvesRow{TeamID: team, ChallengeID: chY, SolvedAt: at, TeamBanned: banned, Scoring: "dynamic", PointsInitial: 500, PointsMin: i32p(100), Decay: i32p(10)}
	}

	store := fakeStore{
		teams: []gen.ListScoreboardTeamsRow{
			{ID: teamA, Name: "Alpha"},
			{ID: teamB, Name: "Bravo"},
			{ID: teamC, Name: "Charlie"},
			{ID: teamD, Name: "Delta", Banned: true},
			{ID: teamE, Name: "Echo"},
		},
		solves: []gen.ListValidSolvesRow{
			staticX(teamA, false, t1), dynY(teamA, false, t3),
			staticX(teamB, false, t2),
			staticX(teamC, false, t4),
			staticX(teamD, true, t1), dynY(teamD, true, t2), // banned: must not count toward Y's solve count
		},
	}

	snap, _, err := compute(context.Background(), store, now)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if !snap.GeneratedAt.Equal(now) {
		t.Errorf("GeneratedAt = %v, want %v", snap.GeneratedAt, now)
	}

	// Y is a dynamic challenge with a single NON-banned solver (Alpha); Delta is
	// banned so does not raise the solve count. value = 500 - (500-100)*1/100 = 496.
	const wantY = 496
	want := []struct {
		team    uuid.UUID
		name    string
		points  int
		solves  int
		rank    *int // nil = unranked (banned)
		hasLast bool
		banned  bool
	}{
		{teamA, "Alpha", 100 + wantY, 2, rankp(1), true, false},
		{teamB, "Bravo", 100, 1, rankp(2), true, false},   // ties Charlie on points; earlier last-solve wins
		{teamC, "Charlie", 100, 1, rankp(3), true, false}, // later last-solve → ranked below Bravo
		{teamE, "Echo", 0, 0, rankp(4), false, false},     // zero-solve team included, ranked last active
		{teamD, "Delta", 100 + wantY, 2, nil, true, true}, // banned: trails, unranked, points still summed
	}
	if len(snap.Standings) != len(want) {
		t.Fatalf("got %d standings rows, want %d: %+v", len(snap.Standings), len(want), snap.Standings)
	}
	for i, w := range want {
		got := snap.Standings[i]
		if got.TeamID != w.team {
			t.Errorf("row %d team = %s (%s), want %s", i, got.TeamID, got.Name, w.team)
		}
		if got.Points != w.points {
			t.Errorf("row %d (%s) points = %d, want %d", i, w.name, got.Points, w.points)
		}
		if got.Solves != w.solves {
			t.Errorf("row %d (%s) solves = %d, want %d", i, w.name, got.Solves, w.solves)
		}
		if got.Banned != w.banned {
			t.Errorf("row %d (%s) banned = %v, want %v", i, w.name, got.Banned, w.banned)
		}
		if (got.Rank == nil) != (w.rank == nil) || (got.Rank != nil && w.rank != nil && *got.Rank != *w.rank) {
			t.Errorf("row %d (%s) rank = %v, want %v", i, w.name, rankStr(got.Rank), rankStr(w.rank))
		}
		if (got.LastSolveAt != nil) != w.hasLast {
			t.Errorf("row %d (%s) hasLastSolve = %v, want %v", i, w.name, got.LastSolveAt != nil, w.hasLast)
		}
	}
}

// TestComputeReadsPluginRecordNotFormula pins the #9 read seam: a plugin-scored challenge is
// scored from the PER-SOLVE recorded value (locked-at-solve), never from the formula and never by
// calling the plugin. Two solvers of the same plugin challenge carry DIFFERENT recorded values —
// impossible under the per-challenge formula, which would give every solver PointsInitial (the
// unknown-mode static fallback). A third solver has a MISSING record (NULL scored_value) and must
// resolve to the deterministic default 0, not to the formula.
func TestComputeReadsPluginRecordNotFormula(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	teamA := uuid.MustParse("00000000-0000-0000-0000-0000000000a1")
	teamB := uuid.MustParse("00000000-0000-0000-0000-0000000000b2")
	teamC := uuid.MustParse("00000000-0000-0000-0000-0000000000c3")
	chP := uuid.MustParse("00000000-0000-0000-0000-0000000000cc") // plugin-scored

	plugin := func(team uuid.UUID, at time.Time, rec *int32) gen.ListValidSolvesRow {
		// PointsInitial is deliberately 999 so a formula fallback (static → 999) is unmistakable.
		return gen.ListValidSolvesRow{TeamID: team, ChallengeID: chP, SolvedAt: at, Scoring: "custom", PointsInitial: 999, ScoredValue: rec}
	}

	store := fakeStore{
		teams: []gen.ListScoreboardTeamsRow{
			{ID: teamA, Name: "Alpha"},
			{ID: teamB, Name: "Bravo"},
			{ID: teamC, Name: "Charlie"},
		},
		solves: []gen.ListValidSolvesRow{
			plugin(teamA, now.Add(-3*time.Hour), i32p(250)),
			plugin(teamB, now.Add(-2*time.Hour), i32p(175)),
			plugin(teamC, now.Add(-1*time.Hour), nil), // MISSING record → 0
		},
	}

	snap, _, err := compute(context.Background(), store, now)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	got := map[uuid.UUID]int{}
	for _, e := range snap.Standings {
		got[e.TeamID] = e.Points
	}
	if got[teamA] != 250 {
		t.Errorf("Alpha points = %d, want 250 (recorded per-solve value, not formula)", got[teamA])
	}
	if got[teamB] != 175 {
		t.Errorf("Bravo points = %d, want 175 (recorded per-solve value, not formula)", got[teamB])
	}
	if got[teamC] != 0 {
		t.Errorf("Charlie points = %d, want 0 (missing record resolves to the deterministic default)", got[teamC])
	}
}

// TestComputeNameTiebreakAmongScorelessTeams checks the final tiebreak: teams with
// identical points and no last-solve are ordered by name.
func TestComputeNameTiebreakAmongScorelessTeams(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	z1 := uuid.MustParse("00000000-0000-0000-0000-0000000000f1")
	z2 := uuid.MustParse("00000000-0000-0000-0000-0000000000f2")
	store := fakeStore{teams: []gen.ListScoreboardTeamsRow{
		{ID: z1, Name: "Zeta"},
		{ID: z2, Name: "Aardvark"},
	}}
	snap, _, err := compute(context.Background(), store, now)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if len(snap.Standings) != 2 || snap.Standings[0].Name != "Aardvark" || snap.Standings[1].Name != "Zeta" {
		t.Fatalf("scoreless teams not ordered by name: %+v", snap.Standings)
	}
}

// TestComputeEmptyBoardIsNonNilSlice guards the "[] not null" invariant that keeps
// WebSocket/API clients reading standings.length from crashing.
func TestComputeEmptyBoardIsNonNilSlice(t *testing.T) {
	snap, _, err := compute(context.Background(), fakeStore{}, time.Now())
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	if snap.Standings == nil {
		t.Fatal("Standings is nil; must be a non-nil empty slice so it serializes as []")
	}
	if len(snap.Standings) != 0 {
		t.Fatalf("empty board produced %d rows", len(snap.Standings))
	}
}

func rankp(n int) *int { return &n }
func rankStr(r *int) string {
	if r == nil {
		return "unranked"
	}
	return "#" + strconv.Itoa(*r)
}
