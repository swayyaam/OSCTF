//go:build integration

package scoreboard

// Phase 5 follow-up (5-i audit finding): the frozen scoreboard must be captured
// exactly once, at freeze onset, and never overwritten by a later racing snapshot.
// MaybeSnapshotFreeze checked the frozen key outside its mutex and wrote with a
// plain SET, so two first-accesses at freeze onset (the public read path and the
// background loop) could both compute-and-write, the later overwriting with a board
// that includes solves landing after the freeze. This test drives that race
// deterministically via a hook and asserts the post-snapshot solve never appears.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/testsupport"
)

func TestFreezeSnapshotWrittenOnceUnderConcurrency(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	q := gen.New(pool)
	ctx := context.Background()

	t0 := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	clk := clock.Clock(func() time.Time { return t0 })
	freezeAt := t0.Add(-time.Minute) // frozen as of t0
	if _, err := q.CreateEvent(ctx, gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: t0.Add(-time.Hour), EndsAt: t0.Add(time.Hour), FreezeAt: &freezeAt,
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	ev := events.New(q, clk)
	sb := New(q, rdb, ev, clk)

	chID := uuid.Must(uuid.NewV7())
	img := "x/y:latest"
	port := int32(8000)
	if _, err := q.CreateChallenge(ctx, gen.CreateChallengeParams{
		ID: chID, Slug: "c1", Title: "C", Category: "web", Kind: "container",
		Flag: "OSCTF{f}", Scoring: "static", PointsInitial: 100,
		Image: &img, InternalPort: &port, MemLimitMb: 128, CpuMillis: 500,
		ContainerEnv: []byte("{}"), Visible: true,
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	mkTeam := func(name string) uuid.UUID {
		uid := uuid.Must(uuid.NewV7())
		if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Username: name + "u", Email: name + "@e.test", PasswordHash: "x", Role: "user"}); err != nil {
			t.Fatalf("user: %v", err)
		}
		tid := uuid.Must(uuid.NewV7())
		if _, err := q.CreateTeam(ctx, gen.CreateTeamParams{ID: tid, Name: name, InviteCode: name + "code", CaptainID: uid}); err != nil {
			t.Fatalf("team: %v", err)
		}
		if err := q.AddTeamMember(ctx, gen.AddTeamMemberParams{TeamID: tid, UserID: uid}); err != nil {
			t.Fatalf("member: %v", err)
		}
		return tid
	}
	team1 := mkTeam("Alpha")
	team2 := mkTeam("Bravo")

	solve := func(team uuid.UUID) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO submissions (id, challenge_id, team_id, user_id, provided, correct)
			 SELECT $1, $2, $3, tm.user_id, 'OSCTF{f}', true
			 FROM team_members tm WHERE tm.team_id = $3 LIMIT 1`,
			uuid.Must(uuid.NewV7()), chID, team); err != nil {
			t.Fatalf("solve: %v", err)
		}
	}

	// team1 solved before the freeze snapshot is taken → belongs in the frozen board.
	solve(team1)

	// Hook after the existence check: park both callers, then hand-sequence them so
	// the first fully writes the frozen board before the second proceeds.
	var idx atomic.Int32
	arrived := make(chan struct{}, 2)
	release1 := make(chan struct{})
	release2 := make(chan struct{})
	afterFrozenExistsCheckHook = func() {
		arrived <- struct{}{}
		if idx.Add(1) == 1 {
			<-release1
		} else {
			<-release2
		}
	}
	defer func() { afterFrozenExistsCheckHook = nil }()

	done := make(chan error, 2)
	run := func() { done <- sb.MaybeSnapshotFreeze(context.Background()) }
	go run()
	go run()

	<-arrived
	<-arrived       // both passed the existence check with keyFrozen absent
	close(release1) // first computes+writes the frozen board (team1 only)
	if err := <-done; err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	solve(team2)    // a solve lands AFTER the frozen board was written
	close(release2) // second proceeds: must NOT overwrite with the later board
	if err := <-done; err != nil {
		t.Fatalf("second snapshot: %v", err)
	}

	snap, ok, err := sb.read(ctx, keyFrozen)
	if err != nil || !ok {
		t.Fatalf("read frozen snapshot: ok=%v err=%v", ok, err)
	}
	points := map[uuid.UUID]int{}
	for _, e := range snap.Standings {
		points[e.TeamID] = e.Points
	}
	if points[team1] == 0 {
		t.Fatalf("frozen board is missing team1's pre-freeze solve (team1=%d)", points[team1])
	}
	if points[team2] != 0 {
		t.Fatalf("frozen board leaked a post-snapshot solve: team2=%d, want 0 — the snapshot was overwritten by a later board", points[team2])
	}
}
