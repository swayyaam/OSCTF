package handlers

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/db/gen"
)

// freezeHidesSolvesAfter reports whether solve activity at/after the freeze point must
// be hidden from this caller for the resource owned by resourceTeam. Public
// solve-exposing routes (getTeam, getUser) use it so they cannot be polled during a
// freeze to reconstruct the standings the frozen scoreboard hides.
//
// Declared visibility during a freeze (also asserted in the policy matrix):
//   - admin                              → live (sees everything)
//   - a member of resourceTeam           → live (own team sees its own progress)
//   - everyone else (incl. anonymous)    → solves before the freeze only
//
// Two failure modes are distinguished deliberately (3a-vii):
//
//   - MISSING events service (nil dependency): a permanent config error. Hide
//     unconditionally (return true with a zero cutoff, which hides every solve) — a
//     security control must not depend on wiring nothing enforces.
//   - A transient READ failure while the service IS wired: neither leak the frozen board
//     (fail open) nor blank every team's solves at once (fail closed) on a single DB
//     blip. Serve the LAST KNOWN freeze state and log loudly. Only when no read has ever
//     succeeded do we fail closed, since we cannot prove the event is not frozen.
//
// resourceTeam nil ⇒ no own-team exception.
func (s *Server) freezeHidesSolvesAfter(ctx context.Context, resourceTeam *uuid.UUID) (bool, time.Time) {
	if s.d.Events == nil {
		return true, time.Time{} // permanent config error → fail closed
	}
	frozen, freezeAt, known := s.freezeState(ctx)
	if !known {
		return true, time.Time{} // transient read error with no cached state → fail closed
	}
	if !frozen {
		return false, time.Time{}
	}
	if admin, aerr := s.callerIsAdmin(ctx); aerr == nil && admin {
		return false, time.Time{}
	}
	if resourceTeam != nil {
		if id, ok := callerID(ctx); ok {
			if member, merr := s.d.Teams.IsMember(ctx, *resourceTeam, id); merr == nil && member {
				return false, time.Time{}
			}
		}
	}
	return true, freezeAt
}

// freezeSnapshot is the last freeze decision successfully read from the events service.
type freezeSnapshot struct {
	frozen   bool
	freezeAt time.Time
}

// freezeState returns the current freeze decision (frozen, cutoff) and whether it is
// known. A successful read updates the cache. A failed read serves the cached value and
// logs loudly; known=false only when nothing has ever been cached, leaving the caller to
// fail closed rather than fail open.
//
// The cache also goes stale in the UNFREEZE direction: a read failure right after an
// admin clears freeze_at keeps hiding until the next successful read. That is the safe
// direction (hide a little too long, never leak), so it is left as-is.
func (s *Server) freezeState(ctx context.Context) (frozen bool, freezeAt time.Time, known bool) {
	e, err := s.d.Events.Get(ctx)
	if err != nil {
		if last := s.lastFreeze.Load(); last != nil {
			s.logger().Warn("freeze visibility: events read failed; serving last known freeze state",
				"error", err.Error(), "frozen", last.frozen)
			return last.frozen, last.freezeAt, true
		}
		s.logger().Warn("freeze visibility: events read failed and no cached state; hiding solves",
			"error", err.Error())
		return false, time.Time{}, false
	}
	fr := s.d.Events.Frozen(e) // Frozen already implies FreezeAt != nil
	var at time.Time
	if e.FreezeAt != nil {
		at = *e.FreezeAt
	}
	s.lastFreeze.Store(&freezeSnapshot{frozen: fr, freezeAt: at})
	return fr, at, true
}

// recompute triggers a scoreboard recompute + broadcast when wired (M5/M6).
func (s *Server) recompute(ctx context.Context) {
	if s.d.Recompute != nil {
		s.d.Recompute(ctx)
	}
}

// requireUser returns the caller identity or ErrUnauthenticated.
func requireUser(ctx context.Context) (auth.Identity, error) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return auth.Identity{}, apperr.ErrUnauthenticated
	}
	return id, nil
}

// requireAdmin resolves the caller and re-reads the user row so promotion,
// demotion, and bans take effect immediately (not on session expiry).
func (s *Server) requireAdmin(ctx context.Context) (gen.User, error) {
	id, err := requireUser(ctx)
	if err != nil {
		return gen.User{}, err
	}
	u, err := s.d.Users.Get(ctx, id.UserID)
	if err != nil {
		return gen.User{}, apperr.ErrUnauthenticated
	}
	if u.Banned || u.Role != "admin" {
		return gen.User{}, apperr.Forbiddenf("admin access required")
	}
	return u, nil
}
