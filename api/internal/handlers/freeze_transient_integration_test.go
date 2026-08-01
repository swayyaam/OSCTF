//go:build integration

package handlers_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/testsupport"
)

// frozenServer builds a minimal handler set over a live, FROZEN event. Only Events is
// needed for the anonymous, teamless freeze-visibility path exercised here.
func frozenServer(t *testing.T) (*handlers.Server, interface{ Close() }) {
	t.Helper()
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	now := time.Now().UTC()
	freeze := now.Add(-30 * time.Minute)
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour), FreezeAt: &freeze,
	}); err != nil {
		t.Fatalf("event: %v", err)
	}
	s := handlers.New(handlers.Deps{Events: events.New(q, clock.System()), Log: discardLog()})
	return s, pool
}

// TestFreezeTransientReadServesLastKnownState (3a-vii): a transient events-read failure
// while the service IS wired must NOT flip freeze visibility. The old code failed OPEN on
// a read error (hide=false → the frozen board leaks through getTeam/getUser during a DB
// blip). It now serves the last known freeze state: once a frozen read has been cached, a
// subsequent failed read still hides post-freeze solves.
func TestFreezeTransientReadServesLastKnownState(t *testing.T) {
	s, pool := frozenServer(t)

	// Warm the cache with a successful (frozen) read.
	hide, cutoff := s.FreezeHidesForTest(context.Background(), nil)
	if !hide || cutoff.IsZero() {
		t.Fatalf("live frozen read: hide=%v cutoff=%v, want hide=true with a non-zero cutoff", hide, cutoff)
	}

	// Simulate a transient read failure: the pool goes away mid-event.
	pool.Close()

	hide2, cutoff2 := s.FreezeHidesForTest(context.Background(), nil)
	if !hide2 {
		t.Fatal("transient events-read failure during a freeze failed OPEN (leaked) — must serve the last known frozen state")
	}
	if !cutoff2.Equal(cutoff) {
		t.Errorf("cached cutoff = %v, want the last known %v", cutoff2, cutoff)
	}
}

// TestFreezeTransientReadWithoutCacheFailsClosed (3a-vii): if the very first read fails —
// no freeze state has ever been cached — the event's freeze status is unknown, so hide
// solves rather than leak (fail closed), distinct from the last-known-state path above.
func TestFreezeTransientReadWithoutCacheFailsClosed(t *testing.T) {
	s, pool := frozenServer(t)
	pool.Close() // fail the first read before anything is cached

	hide, _ := s.FreezeHidesForTest(context.Background(), nil)
	if !hide {
		t.Fatal("events read failed with no cached state — must fail CLOSED (hide solves), not leak")
	}
}
