package handlers

import (
	"context"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
)

// Per-team instance controls and admin fleet observability (v0.2).
//
// M0 stage: the contract exists and the handlers return 501. M4 wires these to
// the scheduler (docs/v0.2/06-api.md, docs/v0.2/10-milestones.md).

// StartInstance starts (or returns) the caller team's instance for a per_team challenge.
func (s *Server) StartInstance(_ context.Context, _ apigen.StartInstanceRequestObject) (apigen.StartInstanceResponseObject, error) {
	return nil, &apperr.NotImplemented{Detail: "per-team instances are not available yet"}
}

// StopInstance stops and destroys the caller team's instance.
func (s *Server) StopInstance(_ context.Context, _ apigen.StopInstanceRequestObject) (apigen.StopInstanceResponseObject, error) {
	return nil, &apperr.NotImplemented{Detail: "per-team instances are not available yet"}
}

// ExtendInstance extends the caller team's instance TTL.
func (s *Server) ExtendInstance(_ context.Context, _ apigen.ExtendInstanceRequestObject) (apigen.ExtendInstanceResponseObject, error) {
	return nil, &apperr.NotImplemented{Detail: "per-team instances are not available yet"}
}

// AdminListInstances lists every instance (shared + per-team).
func (s *Server) AdminListInstances(_ context.Context, _ apigen.AdminListInstancesRequestObject) (apigen.AdminListInstancesResponseObject, error) {
	return nil, &apperr.NotImplemented{Detail: "the instances fleet view is not available yet"}
}

// AdminDestroyInstanceById destroys any instance by ID.
func (s *Server) AdminDestroyInstanceById(_ context.Context, _ apigen.AdminDestroyInstanceByIdRequestObject) (apigen.AdminDestroyInstanceByIdResponseObject, error) {
	return nil, &apperr.NotImplemented{Detail: "destroy-by-id is not available yet"}
}
