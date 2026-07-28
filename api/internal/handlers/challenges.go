package handlers

import (
	"context"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/httpx"
	"github.com/osctf/platform/internal/runtime"
)

// eventStarted reports whether challenges are visible to participants: the event
// phase is past "pre", or the caller is an admin (who previews pre-start).
func (s *Server) eventStarted(ctx context.Context) (bool, error) {
	e, err := s.d.Events.Get(ctx)
	if err != nil {
		return false, err
	}
	if s.d.Events.Phase(e) != events.PhasePre {
		return true, nil
	}
	admin, err := s.callerIsAdmin(ctx)
	if err != nil {
		return false, err
	}
	return admin, nil
}

// ListChallenges returns the participant board once the event has started.
func (s *Server) ListChallenges(ctx context.Context, _ apigen.ListChallengesRequestObject) (apigen.ListChallengesResponseObject, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	started, err := s.eventStarted(ctx)
	if err != nil {
		return nil, err
	}
	if !started {
		return nil, &apperr.Forbidden{Detail: "the event has not started yet"}
	}
	teamID := s.teamIDOrNil(ctx, id.UserID)
	entries, err := s.d.Challenges.ListVisible(ctx, teamID)
	if err != nil {
		return nil, err
	}
	out := make([]apigen.Challenge, 0, len(entries))
	for _, e := range entries {
		c := toChallenge(e)
		s.enrichInstance(ctx, e.Challenge.ID, e.Challenge, &c.HasInstance, &c.ConnectionInfo)
		out = append(out, c)
	}
	return apigen.ListChallenges200JSONResponse(out), nil
}

// GetChallenge returns the full participant detail for a visible challenge.
func (s *Server) GetChallenge(ctx context.Context, request apigen.GetChallengeRequestObject) (apigen.GetChallengeResponseObject, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return nil, err
	}
	started, err := s.eventStarted(ctx)
	if err != nil {
		return nil, err
	}
	if !started {
		return nil, &apperr.Forbidden{Detail: "the event has not started yet"}
	}
	teamID := s.teamIDOrNil(ctx, id.UserID)
	detail, err := s.d.Challenges.GetVisibleDetail(ctx, request.Slug, teamID)
	if err != nil {
		return nil, err
	}
	out := toChallengeDetail(detail)
	s.enrichDetailInstance(ctx, detail.Challenge, teamID, &out)
	return apigen.GetChallenge200JSONResponse(out), nil
}

// enrichDetailInstance fills the detail's instance (and has_instance/connection_info)
// with the caller team's instance for a per_team challenge, or the shared instance
// otherwise. Never exposes the flag.
func (s *Server) enrichDetailInstance(ctx context.Context, ch gen.Challenge, teamID uuid.UUID, out *apigen.ChallengeDetail) {
	if s.d.Runtime == nil || ch.Kind != "container" {
		return
	}
	var inst runtime.Instance
	var ok bool
	if ch.Instancing == "per_team" {
		if teamID == uuid.Nil {
			return
		}
		inst, ok, _ = s.d.Runtime.GetTeamInstance(ctx, ch.ID, teamID)
	} else {
		inst, ok = s.d.Runtime.InstanceForChallenge(ctx, ch.ID)
	}
	if !ok {
		return
	}
	ti := s.toTeamInstance(ch, inst)
	out.Instance = &ti
	out.HasInstance = true
	out.ConnectionInfo = ti.ConnectionInfo
}

// enrichInstance fills has_instance and connection_info for a container challenge
// from the current instance row (no live inspection on the participant path).
func (s *Server) enrichInstance(ctx context.Context, challengeID uuid.UUID, ch gen.Challenge, hasInstance *bool, connInfo **string) {
	if s.d.Runtime == nil || ch.Kind != "container" {
		return
	}
	inst, ok := s.d.Runtime.InstanceForChallenge(ctx, challengeID)
	if !ok {
		return
	}
	*hasInstance = true
	if ci := s.d.Runtime.ConnectionInfo(ch, inst); ci != "" {
		*connInfo = &ci
	}
}

// DownloadAttachment streams an attachment for a visible challenge.
func (s *Server) DownloadAttachment(ctx context.Context, request apigen.DownloadAttachmentRequestObject) (apigen.DownloadAttachmentResponseObject, error) {
	if _, err := requireUser(ctx); err != nil {
		return nil, err
	}
	started, err := s.eventStarted(ctx)
	if err != nil {
		return nil, err
	}
	if !started {
		return nil, &apperr.Forbidden{Detail: "the event has not started yet"}
	}
	// Resolve the challenge by slug, enforce visibility, then open the attachment.
	c, err := s.d.Challenges.GetBySlug(ctx, request.Slug)
	if err != nil {
		return nil, err
	}
	if !c.Visible {
		if admin, aerr := s.callerIsAdmin(ctx); aerr != nil || !admin {
			return nil, apperr.ErrNotFound
		}
	}
	content, err := s.d.Challenges.OpenAttachment(ctx, c.ID, request.Id)
	if err != nil {
		return nil, err
	}
	// The strict response streams Body; set the filename via a response header.
	if p, ok := httpx.HTTPFrom(ctx); ok {
		p.W.Header().Set("Content-Disposition", `attachment; filename="`+content.Filename+`"`)
	}
	return apigen.DownloadAttachment200ApplicationoctetStreamResponse{
		Body:          content.Body,
		ContentLength: content.Size,
	}, nil
}

// teamIDOrNil returns the caller's team ID, or uuid.Nil when teamless.
func (s *Server) teamIDOrNil(ctx context.Context, userID uuid.UUID) uuid.UUID {
	ref, err := s.d.Users.TeamOf(ctx, userID)
	if err != nil || ref == nil {
		return uuid.Nil
	}
	return ref.ID
}
