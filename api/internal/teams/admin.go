package teams

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/pagination"
)

// AdminListResult is a page of teams for the admin panel.
type AdminListResult struct {
	Items []gen.ListTeamsAdminRow
	Total int64
}

// ListAdmin returns a page of teams, optionally filtered by a name search.
func (s *Service) ListAdmin(ctx context.Context, p pagination.Params, query *string) (AdminListResult, error) {
	items, err := s.q.ListTeamsAdmin(ctx, gen.ListTeamsAdminParams{
		Limit: p.Limit(), Offset: p.Offset(), Q: query,
	})
	if err != nil {
		return AdminListResult{}, fmt.Errorf("teams: listing: %w", err)
	}
	total, err := s.q.CountTeamsAdmin(ctx, query)
	if err != nil {
		return AdminListResult{}, fmt.Errorf("teams: counting: %w", err)
	}
	return AdminListResult{Items: items, Total: total}, nil
}

// AdminUpdate toggles a team's banned/hidden flags.
func (s *Service) AdminUpdate(ctx context.Context, teamID uuid.UUID, banned, hidden *bool) (gen.Team, error) {
	t, err := s.q.UpdateTeamAdminFields(ctx, gen.UpdateTeamAdminFieldsParams{
		ID: teamID, Banned: banned, Hidden: hidden,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.Team{}, apperr.ErrNotFound
		}
		return gen.Team{}, fmt.Errorf("teams: admin update: %w", err)
	}
	return t, nil
}

// MemberCount returns the number of members on a team.
func (s *Service) MemberCount(ctx context.Context, teamID uuid.UUID) (int64, error) {
	return s.q.CountTeamMembers(ctx, teamID)
}

// PublicList returns non-hidden teams for the public listing (rank/points added
// by the scoreboard in M5; here member counts and identity only).
func (s *Service) PublicList(ctx context.Context) ([]gen.ListPublicTeamsRow, error) {
	return s.q.ListPublicTeams(ctx)
}
