package handlers

import (
	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/teams"
)

// omitEmptySlice returns nil for an empty slice, else a pointer to it. Optional
// list fields (*[]T with `omitempty`, e.g. AdminInstanceList.Unadopted) must be
// OMITTED when empty, not emitted as [] — the dashboard types them as optional,
// not nullable. Routing every such assignment through this helper makes emitting
// [] at zero structurally impossible, rather than a convention each call site
// must remember (see 4a-i).
func omitEmptySlice[T any](s []T) *[]T {
	if len(s) == 0 {
		return nil
	}
	return &s
}

func toMembers(rows []gen.ListTeamMembersRow) []apigen.Member {
	out := make([]apigen.Member, 0, len(rows))
	for _, r := range rows {
		out = append(out, apigen.Member{Id: r.ID, Username: r.Username})
	}
	return out
}

func toTeamMine(t teams.Team) apigen.TeamMine {
	return apigen.TeamMine{
		Id:         t.Row.ID,
		Name:       t.Row.Name,
		InviteCode: t.Row.InviteCode,
		CaptainId:  t.Row.CaptainID,
		Members:    toMembers(t.Members),
	}
}

func toUserAdmin(r gen.ListUsersAdminRow) apigen.UserAdmin {
	ua := apigen.UserAdmin{
		Id:        r.User.ID,
		Username:  r.User.Username,
		Email:     r.User.Email,
		Role:      apigen.Role(r.User.Role),
		Banned:    r.User.Banned,
		Hidden:    r.User.Hidden,
		CreatedAt: r.User.CreatedAt,
	}
	if r.TeamID != nil && r.TeamName != nil {
		ua.Team = &apigen.TeamRef{Id: *r.TeamID, Name: *r.TeamName}
	}
	return ua
}

func toUserAdminFromRow(u gen.User, team *apigen.TeamRef) apigen.UserAdmin {
	return apigen.UserAdmin{
		Id:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		Role:      apigen.Role(u.Role),
		Banned:    u.Banned,
		Hidden:    u.Hidden,
		Team:      team,
		CreatedAt: u.CreatedAt,
	}
}
