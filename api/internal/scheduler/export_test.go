package scheduler

import (
	"context"

	"github.com/google/uuid"
)

// Test-only accessors, compiled only under `go test` (export_test.go), so the
// production API stays clean while the external scheduler_test package can drive
// deterministic concurrency scenarios.

// LockTeamForTest acquires teamID's lock (see lockTeam). Used by the mutual-exclusion
// test to observe that no two goroutines ever hold the same team's lock.
func (s *Scheduler) LockTeamForTest(ctx context.Context, teamID uuid.UUID) (func(), bool) {
	return s.lockTeam(ctx, teamID)
}

// SetExpireAfterListHookForTest installs a hook ExpireOnce runs after listing the
// expired instances and before destroying any — lets a test inject a racing Extend
// at exactly that point, deterministically, without touching the lock internals.
func (s *Scheduler) SetExpireAfterListHookForTest(f func()) { s.afterList = f }
