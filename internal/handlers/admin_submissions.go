package handlers

import (
	"context"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/pagination"
)

// AdminListSubmissions returns a page of logged submissions with filters.
func (s *Server) AdminListSubmissions(ctx context.Context, request apigen.AdminListSubmissionsRequestObject) (apigen.AdminListSubmissionsResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	p := pagination.Normalize(request.Params.Page, request.Params.PerPage)
	rows, err := s.d.Submissions.ListAdmin(ctx, gen.ListSubmissionsAdminParams{
		Limit: p.Limit(), Offset: p.Offset(),
		ChallengeID: request.Params.ChallengeId,
		TeamID:      request.Params.TeamId,
		UserID:      request.Params.UserId,
		Correct:     request.Params.Correct,
		Since:       request.Params.Since,
		Until:       request.Params.Until,
	})
	if err != nil {
		return nil, err
	}
	total, err := s.d.Submissions.CountAdmin(ctx, gen.CountSubmissionsAdminParams{
		ChallengeID: request.Params.ChallengeId,
		TeamID:      request.Params.TeamId,
		UserID:      request.Params.UserId,
		Correct:     request.Params.Correct,
		Since:       request.Params.Since,
		Until:       request.Params.Until,
	})
	if err != nil {
		return nil, err
	}
	items := make([]apigen.SubmissionAdmin, 0, len(rows))
	for _, r := range rows {
		var ip *string
		if r.Ip != nil {
			s := r.Ip.String()
			ip = &s
		}
		items = append(items, apigen.SubmissionAdmin{
			Id:        r.ID,
			Challenge: apigen.ChallengeRef{Id: r.ChallengeID, Slug: r.ChallengeSlug, Title: r.ChallengeTitle},
			Team:      apigen.TeamRef{Id: r.TeamID, Name: r.TeamName},
			User:      apigen.Member{Id: r.UserID, Username: r.Username},
			Provided:  r.Provided,
			Correct:   r.Correct,
			Ip:        ip,
			CreatedAt: r.CreatedAt,
		})
	}
	return apigen.AdminListSubmissions200JSONResponse(apigen.SubmissionAdminPage{
		Items: items, Total: int(total), Page: p.Page, PerPage: p.PerPage,
	}), nil
}

// AdminGetStats returns the admin dashboard counters.
func (s *Server) AdminGetStats(ctx context.Context, _ apigen.AdminGetStatsRequestObject) (apigen.AdminGetStatsResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	st, err := s.d.Submissions.Stats(ctx)
	if err != nil {
		return nil, err
	}
	// ws_connections comes from the live gauge (M6).
	ws := int(metrics.GaugeValue(metrics.WSConnections))
	return apigen.AdminGetStats200JSONResponse(apigen.AdminStats{
		Users:            int(st.Users),
		Teams:            int(st.Teams),
		Submissions:      int(st.Submissions),
		Solves:           int(st.Solves),
		InstancesRunning: int(st.InstancesRunning),
		WsConnections:    ws,
	}), nil
}
