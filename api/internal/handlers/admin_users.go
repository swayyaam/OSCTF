package handlers

import (
	"context"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/pagination"
	"github.com/osctf/platform/internal/users"
)

// AdminListUsers returns a page of users with filters.
func (s *Server) AdminListUsers(ctx context.Context, request apigen.AdminListUsersRequestObject) (apigen.AdminListUsersResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	p := pagination.Normalize(request.Params.Page, request.Params.PerPage)
	var role *string
	if request.Params.Role != nil {
		r := string(*request.Params.Role)
		role = &r
	}
	res, err := s.d.Users.ListAdmin(ctx, p, users.AdminListFilters{
		Query:  request.Params.Q,
		Banned: request.Params.Banned,
		Hidden: request.Params.Hidden,
		Role:   role,
	})
	if err != nil {
		return nil, err
	}
	items := make([]apigen.UserAdmin, 0, len(res.Items))
	for _, r := range res.Items {
		items = append(items, toUserAdmin(r))
	}
	return apigen.AdminListUsers200JSONResponse(apigen.UserAdminPage{
		Items: items, Total: int(res.Total), Page: p.Page, PerPage: p.PerPage,
	}), nil
}

// AdminUpdateUser toggles banned/hidden or changes role.
func (s *Server) AdminUpdateUser(ctx context.Context, request apigen.AdminUpdateUserRequestObject) (apigen.AdminUpdateUserResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	var role *string
	if request.Body.Role != nil {
		r := string(*request.Body.Role)
		role = &r
	}
	u, err := s.d.Users.AdminUpdate(ctx, actor.ID, request.Id, request.Body.Banned, request.Body.Hidden, role)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, actor.ID, "user.update", "user", request.Id.String(), map[string]any{
		"banned": request.Body.Banned, "hidden": request.Body.Hidden, "role": role,
	})
	var teamRef *apigen.TeamRef
	if ref, terr := s.d.Users.TeamOf(ctx, u.ID); terr == nil && ref != nil {
		teamRef = &apigen.TeamRef{Id: ref.ID, Name: ref.Name}
	}
	return apigen.AdminUpdateUser200JSONResponse(toUserAdminFromRow(u, teamRef)), nil
}

// AdminResetPassword sets a user's password and revokes their sessions.
func (s *Server) AdminResetPassword(ctx context.Context, request apigen.AdminResetPasswordRequestObject) (apigen.AdminResetPasswordResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	if err := s.d.Users.AdminResetPassword(ctx, request.Id, request.Body.NewPassword); err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, actor.ID, "user.password_reset", "user", request.Id.String(), nil)
	return apigen.AdminResetPassword204Response{}, nil
}
