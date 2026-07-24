// Package events is the single-event domain service: reads, admin updates with
// window validation, phase/freeze computation against an injected clock, and
// first-boot default-event seeding. v0.1 enforces exactly one event row.
package events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
)

// Phase is where "now" falls relative to the event window.
type Phase string

const (
	PhasePre     Phase = "pre"
	PhaseRunning Phase = "running"
	PhaseEnded   Phase = "ended"
)

// Service implements event operations.
type Service struct {
	q     *gen.Queries
	clock clock.Clock
}

// New builds the service with an injected clock.
func New(q *gen.Queries, c clock.Clock) *Service {
	return &Service{q: q, clock: c}
}

// Get returns the single event, or ErrNotFound if none exists yet.
func (s *Service) Get(ctx context.Context) (gen.Event, error) {
	e, err := s.q.GetEvent(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.Event{}, apperr.ErrNotFound
		}
		return gen.Event{}, fmt.Errorf("events: get: %w", err)
	}
	return e, nil
}

// Phase computes the event phase at the current time.
func (s *Service) Phase(e gen.Event) Phase {
	now := s.clock()
	switch {
	case now.Before(e.StartsAt):
		return PhasePre
	case now.Before(e.EndsAt):
		return PhaseRunning
	default:
		return PhaseEnded
	}
}

// Frozen reports whether the public scoreboard is currently frozen.
func (s *Service) Frozen(e gen.Event) bool {
	return e.FreezeAt != nil && !s.clock().Before(*e.FreezeAt)
}

// UpdateInput is a partial event update. Nil fields are unchanged. SetFreeze
// distinguishes "leave freeze_at" (false) from "set it to Freeze" (true, incl. nil to clear).
type UpdateInput struct {
	Name        *string
	Description *string
	StartsAt    *time.Time
	EndsAt      *time.Time
	SetFreeze   bool
	FreezeAt    *time.Time
}

// Update applies a partial update after validating the resulting window.
func (s *Service) Update(ctx context.Context, in UpdateInput) (gen.Event, error) {
	cur, err := s.Get(ctx)
	if err != nil {
		return gen.Event{}, err
	}

	// Compute the resulting window to validate before writing.
	starts, ends := cur.StartsAt, cur.EndsAt
	if in.StartsAt != nil {
		starts = *in.StartsAt
	}
	if in.EndsAt != nil {
		ends = *in.EndsAt
	}
	freeze := cur.FreezeAt
	if in.SetFreeze {
		freeze = in.FreezeAt
	}

	v := apperr.NewValidation()
	if !starts.Before(ends) {
		v.Add("ends_at", "must be after starts_at")
	}
	if freeze != nil && (!freeze.After(starts) || freeze.After(ends)) {
		v.Add("freeze_at", "must be within the event window")
	}
	if in.Name != nil && (len(*in.Name) < 1 || len(*in.Name) > 200) {
		v.Add("name", "must be between 1 and 200 characters")
	}
	if err := v.OrNil(); err != nil {
		return gen.Event{}, err
	}

	e, err := s.q.UpdateEvent(ctx, gen.UpdateEventParams{
		ID:          cur.ID,
		Name:        in.Name,
		Description: in.Description,
		StartsAt:    in.StartsAt,
		EndsAt:      in.EndsAt,
		SetFreeze:   in.SetFreeze,
		FreezeAt:    in.FreezeAt,
	})
	if err != nil {
		return gen.Event{}, fmt.Errorf("events: update: %w", err)
	}
	return e, nil
}

// EnsureDefault creates a placeholder event on first boot if none exists.
// Start is 7 days out, end 9 days out — a fresh install never looks "live".
func (s *Service) EnsureDefault(ctx context.Context) error {
	count, err := s.q.CountEvents(ctx)
	if err != nil {
		return fmt.Errorf("events: counting: %w", err)
	}
	if count > 0 {
		return nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("events: generating id: %w", err)
	}
	now := s.clock()
	_, err = s.q.CreateEvent(ctx, gen.CreateEventParams{
		ID:          id,
		Name:        "My CTF",
		Description: "Welcome! An admin can edit this event's name, description, and schedule in the admin panel.",
		StartsAt:    now.Add(7 * 24 * time.Hour),
		EndsAt:      now.Add(9 * 24 * time.Hour),
		FreezeAt:    nil,
	})
	if err != nil {
		return fmt.Errorf("events: creating default: %w", err)
	}
	return nil
}
