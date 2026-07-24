package handlers

import (
	"context"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
)

// CreateTeam creates a team with the caller as captain.
func (s *Server) CreateTeam(ctx context.Context, request apigen.CreateTeamRequestObject) (apigen.CreateTeamResponseObject, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	team, err := s.d.Teams.Create(ctx, id.UserID, request.Body.Name)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, id.UserID, "team.create", "team", team.Row.ID.String(), map[string]any{"name": team.Row.Name})
	return apigen.CreateTeam201JSONResponse(toTeamMine(team)), nil
}

// JoinTeam adds the caller to a team by invite code.
func (s *Server) JoinTeam(ctx context.Context, request apigen.JoinTeamRequestObject) (apigen.JoinTeamResponseObject, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	team, err := s.d.Teams.Join(ctx, id.UserID, request.Body.InviteCode)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, id.UserID, "team.join", "team", team.Row.ID.String(), nil)
	return apigen.JoinTeam200JSONResponse(toTeamMine(team)), nil
}

// LeaveTeam removes the caller from their team.
func (s *Server) LeaveTeam(ctx context.Context, _ apigen.LeaveTeamRequestObject) (apigen.LeaveTeamResponseObject, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.d.Teams.Leave(ctx, id.UserID); err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, id.UserID, "team.leave", "user", id.UserID.String(), nil)
	return apigen.LeaveTeam204Response{}, nil
}

// ListTeams returns non-hidden teams. Rank/points are filled by the scoreboard
// (M5); until then teams show with member counts and zero points.
func (s *Server) ListTeams(ctx context.Context, _ apigen.ListTeamsRequestObject) (apigen.ListTeamsResponseObject, error) {
	rows, err := s.d.Teams.PublicList(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.TeamSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, apigen.TeamSummary{
			Id:          r.ID,
			Name:        r.Name,
			MemberCount: int(r.MemberCount),
			Points:      0,
		})
	}
	return apigen.ListTeams200JSONResponse(out), nil
}

// GetTeam returns a public team page.
func (s *Server) GetTeam(ctx context.Context, request apigen.GetTeamRequestObject) (apigen.GetTeamResponseObject, error) {
	team, err := s.d.Teams.Get(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	detail := apigen.TeamDetail{
		Id:      team.Row.ID,
		Name:    team.Row.Name,
		Members: toMembers(team.Members),
		Points:  0,
		Solves:  []apigen.Solve{},
	}
	// Show the invite code only to a member of this team.
	if id, ok := callerID(ctx); ok {
		member, merr := s.d.Teams.IsMember(ctx, team.Row.ID, id)
		if merr != nil {
			return nil, merr
		}
		if member {
			code := team.Row.InviteCode
			detail.InviteCode = &code
		}
	}
	return apigen.GetTeam200JSONResponse(detail), nil
}

// RenameTeam renames the caller's team; captain-only.
func (s *Server) RenameTeam(ctx context.Context, request apigen.RenameTeamRequestObject) (apigen.RenameTeamResponseObject, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	if _, err := s.d.Teams.RequireCaptain(ctx, request.Id, id.UserID); err != nil {
		return nil, err
	}
	team, err := s.d.Teams.Rename(ctx, request.Id, request.Body.Name)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, id.UserID, "team.rename", "team", request.Id.String(), map[string]any{"name": request.Body.Name})
	return apigen.RenameTeam200JSONResponse(toTeamMine(team)), nil
}

// RegenerateInviteCode issues a fresh invite code; captain-only.
func (s *Server) RegenerateInviteCode(ctx context.Context, request apigen.RegenerateInviteCodeRequestObject) (apigen.RegenerateInviteCodeResponseObject, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := s.d.Teams.RequireCaptain(ctx, request.Id, id.UserID); err != nil {
		return nil, err
	}
	code, err := s.d.Teams.RegenerateInviteCode(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, id.UserID, "team.invite_regen", "team", request.Id.String(), nil)
	return apigen.RegenerateInviteCode200JSONResponse(apigen.InviteCode{InviteCode: code}), nil
}

// callerID returns the caller's user ID if authenticated.
func callerID(ctx context.Context) (uuid.UUID, bool) {
	if id, err := requireUser(ctx); err == nil {
		return id.UserID, true
	}
	return uuid.Nil, false
}
