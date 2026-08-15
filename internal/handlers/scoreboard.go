package handlers

import (
	"context"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/scoreboard"
)

// GetScoreboard returns the standings snapshot. Non-admins during a freeze get
// the frozen snapshot; admins get live data (with frozen=true for the banner).
func (s *Server) GetScoreboard(ctx context.Context, _ apigen.GetScoreboardRequestObject) (apigen.GetScoreboardResponseObject, error) {
	admin, err := s.callerIsAdmin(ctx)
	if err != nil {
		return nil, err
	}
	snap, err := s.d.Scoreboard.Current(ctx, admin)
	if err != nil {
		return nil, err
	}
	return apigen.GetScoreboard200JSONResponse(ToScoreboard(snap)), nil
}

// ToScoreboard maps the internal snapshot to the wire type. Exported so the WS hub
// broadcasts the SAME apigen.Scoreboard the REST endpoint returns — one wire type,
// so WS and REST cannot diverge (they no longer serialize two parallel structs).
func ToScoreboard(snap scoreboard.Snapshot) apigen.Scoreboard {
	standings := make([]apigen.ScoreboardEntry, 0, len(snap.Standings))
	for _, e := range snap.Standings {
		standings = append(standings, apigen.ScoreboardEntry{
			Rank:        e.Rank,
			TeamId:      e.TeamID,
			Name:        e.Name,
			Points:      e.Points,
			LastSolveAt: e.LastSolveAt,
			Solves:      e.Solves,
			Banned:      e.Banned,
		})
	}
	return apigen.Scoreboard{
		Frozen:      snap.Frozen,
		GeneratedAt: snap.GeneratedAt,
		Standings:   standings,
	}
}
