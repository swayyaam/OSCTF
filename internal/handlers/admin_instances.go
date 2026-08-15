package handlers

import (
	"context"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/runtime"
)

// mapRuntimeErr converts a runtime-unavailable error into a 503 domain error.
func mapRuntimeErr(err error) error {
	if err != nil && runtime.IsUnavailable(err) {
		return &apperr.Unavailable{Detail: "the container runtime is unavailable"}
	}
	return err
}

// requireRuntime guards handlers against a missing runtime (defence in depth).
func (s *Server) requireRuntime() error {
	if s.d.Runtime == nil {
		return &apperr.Unavailable{Detail: "the container runtime is not configured"}
	}
	return nil
}

// instancePayload builds the API instance object, rendering connection info from
// the challenge's template.
func (s *Server) instancePayload(ctx context.Context, ch gen.Challenge, inst runtime.Instance) apigen.Instance {
	out := apigen.Instance{
		Id:           inst.ID,
		State:        apigen.InstanceState(inst.State),
		StartedAt:    inst.StartedAt,
		LastHealthAt: inst.LastHealthAt,
	}
	if inst.HostPort != 0 {
		hp := inst.HostPort
		out.HostPort = &hp
	}
	if inst.Err != "" {
		e := inst.Err
		out.Error = &e
	}
	if ci := s.d.Runtime.ConnectionInfo(ch, inst); ci != "" {
		out.ConnectionInfo = &ci
	}
	return out
}

func (s *Server) loadChallenge(ctx context.Context, id uuid.UUID) (gen.Challenge, error) {
	full, err := s.d.Challenges.GetAdmin(ctx, id)
	if err != nil {
		return gen.Challenge{}, err
	}
	return full.Challenge, nil
}

// AdminDeployInstance deploys (or returns the running) instance for a challenge.
func (s *Server) AdminDeployInstance(ctx context.Context, request apigen.AdminDeployInstanceRequestObject) (apigen.AdminDeployInstanceResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireRuntime(); err != nil {
		return nil, err
	}
	inst, err := s.d.Runtime.Deploy(ctx, request.Id)
	if err != nil {
		return nil, mapRuntimeErr(err)
	}
	ch, err := s.loadChallenge(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, actor.ID, "instance.deploy", "instance", inst.ID.String(),
		map[string]any{"challenge_id": request.Id.String(), "state": string(inst.State)})
	return apigen.AdminDeployInstance200JSONResponse(s.instancePayload(ctx, ch, inst)), nil
}

// AdminGetInstance returns the current instance status.
func (s *Server) AdminGetInstance(ctx context.Context, request apigen.AdminGetInstanceRequestObject) (apigen.AdminGetInstanceResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.requireRuntime(); err != nil {
		return nil, err
	}
	inst, ok, err := s.d.Runtime.Get(ctx, request.Id)
	if err != nil {
		return nil, mapRuntimeErr(err)
	}
	if !ok {
		return nil, apperr.ErrNotFound
	}
	ch, err := s.loadChallenge(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	return apigen.AdminGetInstance200JSONResponse(s.instancePayload(ctx, ch, inst)), nil
}

// AdminRestartInstance restarts a challenge's instance.
func (s *Server) AdminRestartInstance(ctx context.Context, request apigen.AdminRestartInstanceRequestObject) (apigen.AdminRestartInstanceResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireRuntime(); err != nil {
		return nil, err
	}
	inst, err := s.d.Runtime.Restart(ctx, request.Id)
	if err != nil {
		return nil, mapRuntimeErr(err)
	}
	ch, err := s.loadChallenge(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	s.d.Audit.Log(ctx, actor.ID, "instance.restart", "instance", inst.ID.String(), nil)
	return apigen.AdminRestartInstance200JSONResponse(s.instancePayload(ctx, ch, inst)), nil
}

// AdminDestroyInstance destroys a challenge's instance and frees its port.
func (s *Server) AdminDestroyInstance(ctx context.Context, request apigen.AdminDestroyInstanceRequestObject) (apigen.AdminDestroyInstanceResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requireRuntime(); err != nil {
		return nil, err
	}
	if err := s.d.Runtime.Destroy(ctx, request.Id); err != nil {
		return nil, mapRuntimeErr(err)
	}
	s.d.Audit.Log(ctx, actor.ID, "instance.destroy", "challenge", request.Id.String(), nil)
	return apigen.AdminDestroyInstance204Response{}, nil
}

// AdminGetInstanceLogs returns recent container output.
func (s *Server) AdminGetInstanceLogs(ctx context.Context, request apigen.AdminGetInstanceLogsRequestObject) (apigen.AdminGetInstanceLogsResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := s.requireRuntime(); err != nil {
		return nil, err
	}
	tail := 200
	if request.Params.Tail != nil {
		tail = *request.Params.Tail
	}
	logs, err := s.d.Runtime.Logs(ctx, request.Id, tail)
	if err != nil {
		return nil, mapRuntimeErr(err)
	}
	return apigen.AdminGetInstanceLogs200JSONResponse(apigen.InstanceLogs{Logs: logs}), nil
}
