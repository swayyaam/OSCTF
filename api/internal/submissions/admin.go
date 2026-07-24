package submissions

import (
	"context"
	"fmt"

	"github.com/osctf/platform/internal/db/gen"
)

// ListAdmin returns a page of logged submissions.
func (s *Service) ListAdmin(ctx context.Context, p gen.ListSubmissionsAdminParams) ([]gen.ListSubmissionsAdminRow, error) {
	return s.q.ListSubmissionsAdmin(ctx, p)
}

// CountAdmin counts submissions matching the filters.
func (s *Service) CountAdmin(ctx context.Context, p gen.CountSubmissionsAdminParams) (int64, error) {
	return s.q.CountSubmissionsAdmin(ctx, p)
}

// Stats aggregates the admin dashboard counters.
type Stats struct {
	Users            int64
	Teams            int64
	Submissions      int64
	Solves           int64
	InstancesRunning int64
}

// Stats returns the dashboard counters (ws connections come from the live gauge).
func (s *Service) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	var err error
	if st.Users, err = s.q.CountUsers(ctx); err != nil {
		return st, fmt.Errorf("submissions: count users: %w", err)
	}
	if st.Teams, err = s.q.CountTeams(ctx); err != nil {
		return st, fmt.Errorf("submissions: count teams: %w", err)
	}
	if st.Submissions, err = s.q.CountSubmissions(ctx); err != nil {
		return st, fmt.Errorf("submissions: count submissions: %w", err)
	}
	if st.Solves, err = s.q.CountSolves(ctx); err != nil {
		return st, fmt.Errorf("submissions: count solves: %w", err)
	}
	if st.InstancesRunning, err = s.q.CountRunningInstances(ctx); err != nil {
		return st, fmt.Errorf("submissions: count instances: %w", err)
	}
	return st, nil
}
