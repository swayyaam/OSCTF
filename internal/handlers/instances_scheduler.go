package handlers

import (
	"context"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/runtime"
)

// Per-team instance controls (participant) and the admin fleet view (v0.2).
// docs/v0.2/06-api.md.

// requireScheduler guards handlers against a missing scheduler (defence in depth).
func (s *Server) requireScheduler() error {
	if s.d.Scheduler == nil {
		return &apperr.Unavailable{Detail: "the instance scheduler is not configured"}
	}
	return nil
}

// callerTeam resolves the authenticated user and their team, requiring both.
func (s *Server) callerTeam(ctx context.Context) (userID, teamID uuid.UUID, err error) {
	id, err := requireUser(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	ref, err := s.d.Users.TeamOf(ctx, id.UserID)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if ref == nil {
		return uuid.Nil, uuid.Nil, &apperr.Forbidden{Detail: "you must be on a team to use instances"}
	}
	return id.UserID, ref.ID, nil
}

// toTeamInstance builds the participant-safe instance payload (never the flag).
func (s *Server) toTeamInstance(ch gen.Challenge, inst runtime.Instance) apigen.TeamInstance {
	out := apigen.TeamInstance{
		Id:        inst.ID,
		State:     apigen.InstanceState(inst.State),
		StartedAt: inst.StartedAt,
		ExpiresAt: inst.ExpiresAt,
	}
	if inst.HostPort != 0 {
		hp := inst.HostPort
		out.HostPort = &hp
	}
	if inst.Err != "" {
		e := inst.Err
		out.Error = &e
	}
	if s.d.Runtime != nil {
		if ci := s.d.Runtime.ConnectionInfo(ch, inst); ci != "" {
			out.ConnectionInfo = &ci
		}
	}
	return out
}

// StartInstance starts (or returns) the caller team's instance.
func (s *Server) StartInstance(ctx context.Context, request apigen.StartInstanceRequestObject) (apigen.StartInstanceResponseObject, error) {
	if err := s.requireScheduler(); err != nil {
		return nil, err
	}
	userID, teamID, err := s.callerTeam(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := s.d.Challenges.GetBySlug(ctx, request.Slug)
	if err != nil {
		return nil, err
	}
	inst, created, err := s.d.Scheduler.Start(ctx, userID, teamID, ch.ID)
	if err != nil {
		return nil, mapRuntimeErr(err)
	}
	payload := s.toTeamInstance(ch, inst)
	if created {
		return apigen.StartInstance201JSONResponse(payload), nil
	}
	return apigen.StartInstance200JSONResponse(payload), nil
}

// StopInstance stops and destroys the caller team's instance.
func (s *Server) StopInstance(ctx context.Context, request apigen.StopInstanceRequestObject) (apigen.StopInstanceResponseObject, error) {
	if err := s.requireScheduler(); err != nil {
		return nil, err
	}
	userID, teamID, err := s.callerTeam(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := s.d.Challenges.GetBySlug(ctx, request.Slug)
	if err != nil {
		return nil, err
	}
	if err := s.d.Scheduler.Stop(ctx, userID, teamID, ch.ID); err != nil {
		return nil, mapRuntimeErr(err)
	}
	return apigen.StopInstance204Response{}, nil
}

// ExtendInstance extends the caller team's instance TTL.
func (s *Server) ExtendInstance(ctx context.Context, request apigen.ExtendInstanceRequestObject) (apigen.ExtendInstanceResponseObject, error) {
	if err := s.requireScheduler(); err != nil {
		return nil, err
	}
	_, teamID, err := s.callerTeam(ctx)
	if err != nil {
		return nil, err
	}
	ch, err := s.d.Challenges.GetBySlug(ctx, request.Slug)
	if err != nil {
		return nil, err
	}
	inst, err := s.d.Scheduler.Extend(ctx, teamID, ch.ID)
	if err != nil {
		return nil, mapRuntimeErr(err)
	}
	return apigen.ExtendInstance200JSONResponse(s.toTeamInstance(ch, inst)), nil
}

// AdminListInstances returns every instance (shared + per-team) for the admin fleet view.
func (s *Server) AdminListInstances(ctx context.Context, _ apigen.AdminListInstancesRequestObject) (apigen.AdminListInstancesResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.requireRuntime(); err != nil {
		return nil, err
	}
	insts, err := s.d.Runtime.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]apigen.AdminInstance, 0, len(insts))
	for _, inst := range insts {
		items = append(items, s.toAdminInstance(ctx, inst))
	}
	list := apigen.AdminInstanceList{Items: items}
	// Surface managed containers reconcile could not resolve to a row (best-effort;
	// a runtime error here must not fail the whole fleet view).
	if unadopted, uerr := s.d.Runtime.ListUnadopted(ctx); uerr == nil {
		us := make([]apigen.UnadoptedContainer, 0, len(unadopted))
		for _, u := range unadopted {
			us = append(us, apigen.UnadoptedContainer{ContainerId: u.ContainerID, Image: u.Image, CreatedAt: u.CreatedAt})
		}
		list.Unadopted = omitEmptySlice(us) // nil (omitted) at zero, never []
	}
	if nets, nerr := s.d.Runtime.ListUnadoptedNetworks(ctx); nerr == nil {
		ns := make([]apigen.UnadoptedNetwork, 0, len(nets))
		for _, n := range nets {
			ns = append(ns, apigen.UnadoptedNetwork{NetworkId: n.NetworkID, Name: n.Name})
		}
		list.UnadoptedNetworks = omitEmptySlice(ns) // nil (omitted) at zero, never []
	}
	return apigen.AdminListInstances200JSONResponse(list), nil
}

// AdminDestroyInstanceById destroys any instance by id.
func (s *Server) AdminDestroyInstanceById(ctx context.Context, request apigen.AdminDestroyInstanceByIdRequestObject) (apigen.AdminDestroyInstanceByIdResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireRuntime(); err != nil {
		return nil, err
	}
	if err := s.d.Runtime.DestroyInstance(ctx, request.Id); err != nil {
		return nil, mapRuntimeErr(err)
	}
	s.d.Audit.Log(ctx, actor.ID, "instance.cleanup", "instance", request.Id.String(), map[string]any{"reason": "admin"})
	return apigen.AdminDestroyInstanceById204Response{}, nil
}

// toAdminInstance maps an instance to the admin payload (never the flag),
// resolving challenge slug and team name for display.
func (s *Server) toAdminInstance(ctx context.Context, inst runtime.Instance) apigen.AdminInstance {
	out := apigen.AdminInstance{
		Id:           inst.ID,
		ChallengeId:  inst.ChallengeID,
		State:        apigen.InstanceState(inst.State),
		TeamId:       inst.TeamID,
		StartedAt:    inst.StartedAt,
		ExpiresAt:    inst.ExpiresAt,
		LastHealthAt: inst.LastHealthAt,
	}
	if inst.HostPort != 0 {
		hp := inst.HostPort
		out.HostPort = &hp
	}
	if inst.Network != "" {
		n := inst.Network
		out.Network = &n
	}
	if inst.Err != "" {
		e := inst.Err
		out.Error = &e
	}
	if ch, err := s.d.Challenges.GetByID(ctx, inst.ChallengeID); err == nil {
		out.ChallengeSlug = &ch.Slug
	}
	if inst.TeamID != nil {
		if team, err := s.d.Teams.GetRow(ctx, *inst.TeamID); err == nil {
			name := string(team.Name)
			out.TeamName = &name
		}
	}
	return out
}
