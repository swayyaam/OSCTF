package handlers

import (
	"context"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
)

// GetEvent returns the public event info (no freeze timestamp).
func (s *Server) GetEvent(ctx context.Context, _ apigen.GetEventRequestObject) (apigen.GetEventResponseObject, error) {
	e, err := s.d.Events.Get(ctx)
	if err != nil {
		return nil, err
	}
	return apigen.GetEvent200JSONResponse(apigen.Event{
		Name:        e.Name,
		Description: e.Description,
		StartsAt:    e.StartsAt,
		EndsAt:      e.EndsAt,
		Frozen:      s.d.Events.Frozen(e),
		Phase:       apigen.EventPhase(s.d.Events.Phase(e)),
	}), nil
}

// AdminGetEvent returns the full event including the freeze timestamp.
func (s *Server) AdminGetEvent(ctx context.Context, _ apigen.AdminGetEventRequestObject) (apigen.AdminGetEventResponseObject, error) {
	if _, err := s.requireAdmin(ctx); err != nil {
		return nil, err
	}
	e, err := s.d.Events.Get(ctx)
	if err != nil {
		return nil, err
	}
	return apigen.AdminGetEvent200JSONResponse(toEventAdmin(e)), nil
}

// AdminUpdateEvent applies a partial event update.
//
// Note: the freeze_at field cannot be distinguished between "absent" and
// "explicit null" after JSON decoding, so every PATCH sets the freeze value
// (the admin UI always submits the whole event form). See the M4 decision log.
func (s *Server) AdminUpdateEvent(ctx context.Context, request apigen.AdminUpdateEventRequestObject) (apigen.AdminUpdateEventResponseObject, error) {
	actor, err := s.requireAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if request.Body == nil {
		return nil, apperr.ErrBadRequest
	}
	e, err := s.d.Events.Update(ctx, events.UpdateInput{
		Name:        request.Body.Name,
		Description: request.Body.Description,
		StartsAt:    request.Body.StartsAt,
		EndsAt:      request.Body.EndsAt,
		SetFreeze:   true,
		FreezeAt:    request.Body.FreezeAt,
	})
	if err != nil {
		return nil, err
	}
	// Clearing freeze_at deletes the frozen snapshot so the board jumps to live
	// (docs/v0.1/07-scoring.md).
	if e.FreezeAt == nil && s.d.Scoreboard != nil {
		if cerr := s.d.Scoreboard.ClearFreeze(ctx); cerr != nil {
			return nil, cerr
		}
	}
	s.d.Audit.Log(ctx, actor.ID, "event.update", "event", e.ID.String(), nil)
	s.recompute(ctx)
	return apigen.AdminUpdateEvent200JSONResponse(toEventAdmin(e)), nil
}

func toEventAdmin(e gen.Event) apigen.EventAdmin {
	return apigen.EventAdmin{
		Id:          e.ID,
		Name:        e.Name,
		Description: e.Description,
		StartsAt:    e.StartsAt,
		EndsAt:      e.EndsAt,
		FreezeAt:    e.FreezeAt,
	}
}
