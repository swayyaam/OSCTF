package handlers

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// FreezeHidesForTest exposes the unexported freeze-visibility decision so tests can
// assert it directly (notably the fail-closed nil-Events path) without a full stack.
func (s *Server) FreezeHidesForTest(ctx context.Context, team *uuid.UUID) (bool, time.Time) {
	return s.freezeHidesSolvesAfter(ctx, team)
}
