package handlers

import (
	"context"
	"strconv"
	"time"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/submissions"
)

// SubmitFlag handles a flag submission for the caller's team.
func (s *Server) SubmitFlag(ctx context.Context, request apigen.SubmitFlagRequestObject) (apigen.SubmitFlagResponseObject, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}

	teamRef, err := s.d.Users.TeamOf(ctx, id.UserID)
	if err != nil {
		return nil, err
	}
	if teamRef == nil {
		return nil, &apperr.Forbidden{Detail: "you must be on a team to submit flags"}
	}

	// Rate limits: 10/min per (team, challenge), 30/min per user.
	if err := s.limit(ctx, "sub-team-chal", teamRef.ID.String()+":"+request.Slug, 10, time.Minute); err != nil {
		return nil, err
	}
	if err := s.limit(ctx, "sub-user", id.UserID.String(), 30, time.Minute); err != nil {
		return nil, err
	}

	teamRow, err := s.d.Teams.GetRow(ctx, teamRef.ID)
	if err != nil {
		return nil, err
	}
	admin, err := s.callerIsAdmin(ctx)
	if err != nil {
		return nil, err
	}

	ip := s.clientIP(ctx)
	var ipp *string
	if ip != "" {
		ipp = &ip
	}

	res, err := s.d.Submissions.Submit(ctx, submissions.Input{
		UserID: id.UserID, TeamID: teamRef.ID, Slug: request.Slug,
		Flag: request.Body.Flag, IP: ipp,
		TeamBanned: teamRow.Banned, BypassWindow: admin,
	})
	if err != nil {
		return nil, err
	}

	metrics.Submissions.WithLabelValues(strconv.FormatBool(res.Correct)).Inc()
	if res.Correct {
		s.recompute(ctx)
	}
	return apigen.SubmitFlag200JSONResponse(apigen.SubmitResult{
		Correct: res.Correct,
		Points:  res.Points,
	}), nil
}
