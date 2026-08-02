// Package scoreboard computes standings from the ground truth (recompute-from-
// scratch, not incremental), caches the snapshot in Redis, and manages the freeze
// snapshot. The decay math lives in the scoring package; this package only counts
// solves and sums values (docs/v0.1/07-scoring.md).
package scoreboard

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/scoring"
)

// Entry is one standings row. This is the internal/cache shape; the wire shape is
// apigen.ScoreboardEntry, which BOTH the REST endpoint and the WS hub now serialize
// (via handlers.ToScoreboard), so WS↔REST byte-identity no longer depends on this
// struct's field order. The order is left matching apigen for tidiness only.
type Entry struct {
	Banned      bool       `json:"banned"`
	LastSolveAt *time.Time `json:"last_solve_at"`
	Name        string     `json:"name"`
	Points      int        `json:"points"`
	Rank        *int       `json:"rank"`
	Solves      int        `json:"solves"`
	TeamID      uuid.UUID  `json:"team_id"`
}

// Snapshot is a full standings snapshot (serialized to Redis and the API).
type Snapshot struct {
	Frozen      bool      `json:"frozen"`
	GeneratedAt time.Time `json:"generated_at"`
	Standings   []Entry   `json:"standings"`
}

// compute runs the one true standings algorithm against the current DB state.
func compute(ctx context.Context, q *gen.Queries, now time.Time) (Snapshot, error) {
	teamRows, err := q.ListScoreboardTeams(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("scoreboard: listing teams: %w", err)
	}
	solveRows, err := q.ListValidSolves(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("scoreboard: listing solves: %w", err)
	}

	// Step 2: solve count per challenge, counting non-banned teams only.
	type chalParams struct {
		mode   string
		params scoring.ChallengeScoring
	}
	solveCount := map[uuid.UUID]int{}
	chal := map[uuid.UUID]chalParams{}
	for _, r := range solveRows {
		if _, ok := chal[r.ChallengeID]; !ok {
			cp := chalParams{mode: r.Scoring, params: scoring.ChallengeScoring{Initial: int(r.PointsInitial)}}
			if r.PointsMin != nil {
				cp.params.Min = int(*r.PointsMin)
			}
			if r.Decay != nil {
				cp.params.Decay = int(*r.Decay)
			}
			chal[r.ChallengeID] = cp
		}
		if !r.TeamBanned {
			solveCount[r.ChallengeID]++
		}
	}

	// value[challenge] = engine.Value(params, solveCount).
	value := make(map[uuid.UUID]int, len(chal))
	for id, cp := range chal {
		value[id] = scoring.Value(cp.mode, cp.params, solveCount[id])
	}

	// Step 3: aggregate per team.
	type agg struct {
		points    int
		solves    int
		lastSolve time.Time
	}
	byTeam := map[uuid.UUID]*agg{}
	for _, r := range solveRows {
		a := byTeam[r.TeamID]
		if a == nil {
			a = &agg{}
			byTeam[r.TeamID] = a
		}
		a.points += value[r.ChallengeID]
		a.solves++
		if r.SolvedAt.After(a.lastSolve) {
			a.lastSolve = r.SolvedAt
		}
	}

	// Build entries for every non-hidden team (zero-solve teams included).
	var active, banned []Entry
	for _, t := range teamRows {
		e := Entry{TeamID: t.ID, Name: t.Name, Banned: t.Banned}
		if a := byTeam[t.ID]; a != nil {
			e.Points = a.points
			e.Solves = a.solves
			ls := a.lastSolve
			e.LastSolveAt = &ls
		}
		if t.Banned {
			banned = append(banned, e)
		} else {
			active = append(active, e)
		}
	}

	// Step 5: sort non-banned by points desc, last-solve asc, name asc.
	sort.SliceStable(active, func(i, j int) bool {
		if active[i].Points != active[j].Points {
			return active[i].Points > active[j].Points
		}
		li, lj := active[i].LastSolveAt, active[j].LastSolveAt
		switch {
		case li == nil && lj != nil:
			return true // scoreless before those with a solve? both 0 points here
		case li != nil && lj == nil:
			return false
		case li != nil && lj != nil && !li.Equal(*lj):
			return li.Before(*lj)
		}
		return active[i].Name < active[j].Name
	})
	for i := range active {
		rank := i + 1
		active[i].Rank = &rank
	}

	// Banned teams trail, unranked, in a deterministic order.
	sort.SliceStable(banned, func(i, j int) bool { return banned[i].Name < banned[j].Name })

	// Always a non-nil slice so it serializes as [] (not null) — a null here
	// crashes clients that read standings.length (e.g. over the WebSocket).
	standings := make([]Entry, 0, len(active)+len(banned))
	standings = append(standings, active...)
	standings = append(standings, banned...)

	return Snapshot{
		GeneratedAt: now,
		Standings:   standings,
	}, nil
}
