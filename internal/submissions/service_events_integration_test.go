//go:build integration

package submissions_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/swayyaam/OSCTF/internal/db/gen"
	"github.com/swayyaam/OSCTF/internal/events"
	"github.com/swayyaam/OSCTF/internal/submissions"
	"github.com/swayyaam/OSCTF/internal/testsupport"
)

// A correct solve publishes exactly one challenge.solved event, post-commit, with the solve's ids
// and NO secret (never the flag). A wrong submission publishes nothing.
func TestChallengeSolvedEventPublished(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	chID, slug := seedScoredChallenge(t, pool, q, "static", "OSCTF{event_flag}")
	team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
	user := captainOf(t, q, team)

	bus := events.NewBus()
	got := make(chan events.Event, 4)
	cancel := bus.Subscribe("test", func(string) bool { return true }, func(_ context.Context, e events.Event) error {
		got <- e
		return nil
	})
	defer cancel()
	svc := newSubmissionsService(t, pool).WithBus(bus)

	// A wrong submission must publish nothing.
	if _, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "wrong"}); err != nil {
		t.Fatalf("submit wrong: %v", err)
	}
	// A correct solve publishes challenge.solved.
	if _, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "OSCTF{event_flag}"}); err != nil {
		t.Fatalf("submit correct: %v", err)
	}

	select {
	case e := <-got:
		if e.Name != "challenge.solved" {
			t.Errorf("event name = %q, want challenge.solved", e.Name)
		}
		if e.Data["challenge_id"] != chID.String() {
			t.Errorf("challenge_id = %q, want %s", e.Data["challenge_id"], chID)
		}
		if e.Data["team_id"] != team.String() {
			t.Errorf("team_id = %q, want %s", e.Data["team_id"], team)
		}
		if e.ID == "" {
			t.Error("event ID empty (needed for dedup)")
		}
		for k, v := range e.Data {
			if strings.Contains(v, "OSCTF{") {
				t.Errorf("event leaked a flag in data[%q] = %q", k, v)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no challenge.solved event for a correct solve")
	}

	// The wrong submission must not have produced an event: exactly one event total.
	select {
	case e := <-got:
		t.Errorf("unexpected extra event %+v — a wrong submission must publish nothing", e)
	case <-time.After(200 * time.Millisecond):
	}
}
