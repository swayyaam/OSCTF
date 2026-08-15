package handlers

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
)

// GetUser returns a public user profile. Hidden users are 404 to non-admins.
// The solves list is populated in M5 (scoring); it is empty until then.
func (s *Server) GetUser(ctx context.Context, request apigen.GetUserRequestObject) (apigen.GetUserResponseObject, error) {
	u, err := s.d.Users.Get(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	if u.Hidden {
		// Do not reveal hidden users to non-admins.
		if admin, aerr := s.callerIsAdmin(ctx); aerr != nil || !admin {
			return nil, apperr.ErrNotFound
		}
	}
	solveRows, err := s.d.Users.Solves(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	// Own team (the viewed user's team) sees its own progress live during a freeze;
	// everyone else sees pre-freeze solves only. Fetch the team up front so it also
	// drives the freeze check, not just the profile payload.
	team, err := s.d.Users.TeamOf(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	var resourceTeam *uuid.UUID
	if team != nil {
		resourceTeam = &team.ID
	}
	hide, cutoff := s.freezeHidesSolvesAfter(ctx, resourceTeam)
	solves := make([]apigen.Solve, 0, len(solveRows))
	for _, r := range solveRows {
		if hide && !r.SolvedAt.Before(cutoff) {
			continue
		}
		pts, perr := s.d.Challenges.CurrentValue(ctx, r.ChallengeID)
		if perr != nil {
			return nil, perr
		}
		solves = append(solves, apigen.Solve{
			ChallengeId: r.ChallengeID, Slug: r.Slug, Title: r.Title,
			Category: apigen.Category(r.Category), Points: pts, SolvedAt: r.SolvedAt,
		})
	}
	profile := apigen.PublicUser{Id: u.ID, Username: u.Username, Solves: solves}
	if team != nil {
		profile.Team = &apigen.TeamRef{Id: team.ID, Name: team.Name}
	}
	return apigen.GetUser200JSONResponse(profile), nil
}

// callerIsAdmin reports whether the caller is an admin (re-reads the user row).
// Unauthenticated or forbidden means "not admin"; other errors propagate.
func (s *Server) callerIsAdmin(ctx context.Context) (bool, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		if errors.Is(err, apperr.ErrUnauthenticated) || errors.Is(err, apperr.ErrForbidden) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
