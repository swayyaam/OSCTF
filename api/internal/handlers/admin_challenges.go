package handlers

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/pagination"
	"github.com/osctf/platform/internal/runtime"
)

// AdminListChallenges returns a page of all challenges (including invisible).
func (s *Server) AdminListChallenges(ctx context.Context, request apigen.AdminListChallengesRequestObject) (apigen.AdminListChallengesResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	p := pagination.Normalize(request.Params.Page, request.Params.PerPage)
	var cat, kind *string
	if request.Params.Category != nil {
		c := string(*request.Params.Category)
		cat = &c
	}
	if request.Params.Kind != nil {
		k := string(*request.Params.Kind)
		kind = &k
	}
	res, err := s.d.Challenges.ListAdmin(ctx, p, challenges.AdminListFilters{
		Category: cat, Visible: request.Params.Visible, Kind: kind,
	})
	if err != nil {
		return nil, err
	}
	items := make([]apigen.ChallengeAdmin, 0, len(res.Items))
	for _, f := range res.Items {
		items = append(items, toChallengeAdmin(f))
	}
	return apigen.AdminListChallenges200JSONResponse(apigen.ChallengeAdminPage{
		Items: items, Total: int(res.Total), Page: p.Page, PerPage: p.PerPage,
	}), nil
}

// AdminCreateChallenge creates a challenge.
func (s *Server) AdminCreateChallenge(ctx context.Context, request apigen.AdminCreateChallengeRequestObject) (apigen.AdminCreateChallengeResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	b := request.Body
	in := challenges.CreateInput{
		Slug: b.Slug, Title: b.Title, Category: string(b.Category),
		Flag: b.Flag, PointsInitial: b.PointsInitial,
		PointsMin: b.PointsMin, Decay: b.Decay, MaxAttempts: b.MaxAttempts,
		Image: b.Image, InternalPort: b.InternalPort,
		MemLimitMB: b.MemLimitMb, CPUMillis: b.CpuMillis,
		ConnectionTemplate: b.ConnectionTemplate,
	}
	if b.Description != nil {
		in.Description = *b.Description
	}
	if b.Difficulty != nil {
		d := string(*b.Difficulty)
		in.Difficulty = &d
	}
	if b.Kind != nil {
		in.Kind = string(*b.Kind)
	}
	if b.FlagCaseInsensitive != nil {
		in.FlagCaseInsensitive = *b.FlagCaseInsensitive
	}
	if b.Scoring != nil {
		in.Scoring = string(*b.Scoring)
	}
	if b.Visible != nil {
		in.Visible = *b.Visible
	}
	if b.ContainerEnv != nil {
		in.ContainerEnv = *b.ContainerEnv
	}
	c, err := s.d.Challenges.Create(ctx, in)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, actor.ID, "challenge.create", "challenge", c.ID.String(), map[string]any{"slug": c.Slug})
	full, err := s.d.Challenges.GetAdmin(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	s.recompute(ctx)
	return apigen.AdminCreateChallenge201JSONResponse(toChallengeAdmin(full)), nil
}

// AdminGetChallenge returns full challenge detail (including the flag).
func (s *Server) AdminGetChallenge(ctx context.Context, request apigen.AdminGetChallengeRequestObject) (apigen.AdminGetChallengeResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	full, err := s.d.Challenges.GetAdmin(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	return apigen.AdminGetChallenge200JSONResponse(toChallengeAdmin(full)), nil
}

// AdminUpdateChallenge applies a partial update.
//
// Note: nullable fields are set when present; clearing a field to null via PATCH
// is not supported in v0.1 (send a value or leave it out). See the M4 decision log.
func (s *Server) AdminUpdateChallenge(ctx context.Context, request apigen.AdminUpdateChallengeRequestObject) (apigen.AdminUpdateChallengeResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	b := request.Body
	in := challenges.UpdateInput{
		Slug: b.Slug, Title: b.Title, Description: b.Description,
		Flag: b.Flag, FlagCaseInsensitive: b.FlagCaseInsensitive,
		PointsInitial: b.PointsInitial, Visible: b.Visible,
		MemLimitMB: b.MemLimitMb, CPUMillis: b.CpuMillis,
		ContainerEnv: derefEnv(b.ContainerEnv),
	}
	if b.Category != nil {
		c := string(*b.Category)
		in.Category = &c
	}
	if b.Difficulty != nil {
		d := string(*b.Difficulty)
		in.SetDifficulty = true
		in.Difficulty = &d
	}
	if b.Scoring != nil {
		sc := string(*b.Scoring)
		in.Scoring = &sc
	}
	if b.PointsMin != nil {
		in.SetPointsMin = true
		in.PointsMin = b.PointsMin
	}
	if b.Decay != nil {
		in.SetDecay = true
		in.Decay = b.Decay
	}
	if b.MaxAttempts != nil {
		in.SetMaxAttempts = true
		in.MaxAttempts = b.MaxAttempts
	}
	if b.Image != nil {
		in.SetImage = true
		in.Image = b.Image
	}
	if b.InternalPort != nil {
		in.SetInternalPort = true
		in.InternalPort = b.InternalPort
	}
	if b.ConnectionTemplate != nil {
		in.SetConnectionTmpl = true
		in.ConnectionTemplate = b.ConnectionTemplate
	}
	c, err := s.d.Challenges.Update(ctx, request.Id, in)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, actor.ID, "challenge.update", "challenge", c.ID.String(), nil)
	full, err := s.d.Challenges.GetAdmin(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	s.recompute(ctx)
	return apigen.AdminUpdateChallenge200JSONResponse(toChallengeAdmin(full)), nil
}

// AdminDeleteChallenge deletes a challenge (confirm required when visible during a
// running event). The instance is destroyed by the runtime in M7 before deletion.
func (s *Server) AdminDeleteChallenge(ctx context.Context, request apigen.AdminDeleteChallengeRequestObject) (apigen.AdminDeleteChallengeResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	running := false
	if e, eerr := s.d.Events.Get(ctx); eerr == nil {
		running = s.d.Events.Phase(e) == events.PhaseRunning
	}
	confirm := request.Params.Confirm != nil && *request.Params.Confirm
	if err := s.destroyInstanceIfAny(ctx, request.Id); err != nil {
		return nil, err
	}
	if err := s.d.Challenges.Delete(ctx, request.Id, running, confirm); err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, actor.ID, "challenge.delete", "challenge", request.Id.String(), nil)
	s.recompute(ctx)
	return apigen.AdminDeleteChallenge204Response{}, nil
}

// destroyInstanceIfAny destroys the challenge's instance (if any) before deletion.
// A runtime-unavailable error is tolerated so a challenge can still be removed
// while Docker is down (the orphan container, if any, is cleaned on reconcile).
func (s *Server) destroyInstanceIfAny(ctx context.Context, challengeID uuid.UUID) error {
	if s.d.Runtime == nil {
		return nil
	}
	if err := s.d.Runtime.DestroyForChallenge(ctx, challengeID); err != nil && !runtime.IsUnavailable(err) {
		return mapRuntimeErr(err)
	}
	return nil
}

// AdminUploadAttachment stores an uploaded file as a challenge attachment.
func (s *Server) AdminUploadAttachment(ctx context.Context, request apigen.AdminUploadAttachmentRequestObject) (apigen.AdminUploadAttachmentResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	part, err := request.Body.NextPart()
	if err != nil {
		return nil, fmt.Errorf("%w: expected a 'file' part", apperr.ErrBadRequest)
	}
	defer func() { _ = part.Close() }()
	if part.FormName() != "file" {
		return nil, fmt.Errorf("%w: the multipart field must be named 'file'", apperr.ErrBadRequest)
	}
	filename := part.FileName()
	if filename == "" {
		return nil, fmt.Errorf("%w: the uploaded file has no name", apperr.ErrBadRequest)
	}

	// Enforce the size cap while buffering to a temp-free limited reader.
	maxBytes := int64(s.d.MaxAttachmentMB) * 1024 * 1024
	buf, err := io.ReadAll(io.LimitReader(part, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("challenges: reading upload: %w", err)
	}
	if int64(len(buf)) > maxBytes {
		v := apperr.NewValidation()
		v.Add("file", fmt.Sprintf("exceeds the %d MB limit", s.d.MaxAttachmentMB))
		return nil, v
	}
	contentType := part.Header.Get("Content-Type")

	att, err := s.d.Challenges.UploadAttachment(ctx, request.Id, filename, contentType, int64(len(buf)), bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, actor.ID, "challenge.attachment.upload", "challenge", request.Id.String(), map[string]any{"filename": filename})
	return apigen.AdminUploadAttachment201JSONResponse(toAttachmentAdmin(att)), nil
}

// AdminDeleteAttachment removes an attachment.
func (s *Server) AdminDeleteAttachment(ctx context.Context, request apigen.AdminDeleteAttachmentRequestObject) (apigen.AdminDeleteAttachmentResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.d.Challenges.DeleteAttachment(ctx, request.Id, request.Aid); err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, actor.ID, "challenge.attachment.delete", "attachment", request.Aid.String(), nil)
	return apigen.AdminDeleteAttachment204Response{}, nil
}

func derefEnv(p *map[string]string) map[string]string {
	if p == nil {
		return nil
	}
	return *p
}
