package handlers

import (
	"context"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/pagination"
)

// AdminListTeams returns a page of teams.
func (s *Server) AdminListTeams(ctx context.Context, request apigen.AdminListTeamsRequestObject) (apigen.AdminListTeamsResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	p := pagination.Normalize(request.Params.Page, request.Params.PerPage)
	res, err := s.d.Teams.ListAdmin(ctx, p, request.Params.Q)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.TeamAdmin, 0, len(res.Items))
	for _, r := range res.Items {
		items = append(items, apigen.TeamAdmin{
			Id:          r.ID,
			Name:        r.Name,
			Banned:      r.Banned,
			Hidden:      r.Hidden,
			MemberCount: int(r.MemberCount),
			CreatedAt:   r.CreatedAt,
		})
	}
	return apigen.AdminListTeams200JSONResponse(apigen.TeamAdminPage{
		Items: items, Total: int(res.Total), Page: p.Page, PerPage: p.PerPage,
	}), nil
}

// AdminUpdateTeam toggles a team's banned/hidden flags.
func (s *Server) AdminUpdateTeam(ctx context.Context, request apigen.AdminUpdateTeamRequestObject) (apigen.AdminUpdateTeamResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	t, err := s.d.Teams.AdminUpdate(ctx, request.Id, request.Body.Banned, request.Body.Hidden)
	if err != nil {
		return nil, err
	}
	count, err := s.d.Teams.MemberCount(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, actor.ID, "team.update", "team", request.Id.String(), map[string]any{
		"banned": request.Body.Banned, "hidden": request.Body.Hidden,
	})
	return apigen.AdminUpdateTeam200JSONResponse(apigen.TeamAdmin{
		Id:          t.ID,
		Name:        t.Name,
		Banned:      t.Banned,
		Hidden:      t.Hidden,
		MemberCount: int(count),
		CreatedAt:   t.CreatedAt,
	}), nil
}
