package handlers

import (
	"context"
	"errors"

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
	profile := apigen.PublicUser{
		Id:       u.ID,
		Username: u.Username,
		Solves:   []apigen.Solve{},
	}
	team, err := s.d.Users.TeamOf(ctx, u.ID)
	if err != nil {
		return nil, err
	}
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
