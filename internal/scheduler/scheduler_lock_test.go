package scheduler_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/scheduler"
)

// TestTeamLockMutualExclusionUnderChurn asserts the behaviour that the refcount
// registry must guarantee: no two goroutines ever hold the same team's lock at once,
// even while entries are constantly created and evicted. Each holder bumps a per-team
// counter and fails the test if it is ever not exactly 1 — which is what would happen
// if the eviction hazard let two goroutines resolve different lock structs for one
// team and both enter. Pure in-memory; run at -count=50 -race.
func TestTeamLockMutualExclusionUnderChurn(t *testing.T) {
	s := scheduler.New(nil, nil, nil, nil, nil, nil, nil, scheduler.Config{})
	ctx := context.Background()

	const teams = 4
	ids := make([]uuid.UUID, teams)
	for i := range ids {
		ids[i] = uuid.New()
	}
	held := make([]atomic.Int32, teams)
	var breaches atomic.Int64

	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for it := 0; it < 400; it++ {
				k := (g + it) % teams // vary the team so entries churn and evict
				unlock, ok := s.LockTeamForTest(ctx, ids[k])
				if !ok {
					breaches.Add(1) // a never-cancelled context must always acquire
					return
				}
				if held[k].Add(1) != 1 {
					breaches.Add(1) // another goroutine is in the same team's lock
				}
				held[k].Add(-1)
				unlock()
			}
		}(g)
	}
	wg.Wait()

	if n := breaches.Load(); n != 0 {
		t.Fatalf("mutual exclusion breached %d times (two goroutines in one team's lock, or a failed acquire)", n)
	}
}
