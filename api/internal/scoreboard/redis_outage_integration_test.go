//go:build integration

package scoreboard

// Redis-outage behavior (docs THREAT_MODEL/INVARIANTS): a live scoreboard read DEGRADES to a direct
// Postgres recompute when the Redis cache is unavailable — a slightly slower board beats a dark one
// mid-event — while a FROZEN read FAILS CLOSED, because the frozen snapshot lives only in Redis and
// has no Postgres authority to fall back to. This pins that the two paths behave DIFFERENTLY under
// the SAME outage (so a future change can't quietly unify them), and that both recover when Redis
// returns — using a real container pause, which exercises the real timeout/reconnect paths.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/testsupport"
)

func TestScoreboardRedisOutageDegradesButFreezeFailsClosedIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb, pause, unpause := testsupport.RedisPausable(t)
	q := gen.New(pool)
	ctx := context.Background()

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var nowNano atomic.Int64
	nowNano.Store(base.UnixNano())
	var clk clock.Clock = func() time.Time { return time.Unix(0, nowNano.Load()).UTC() }

	freezeAt := base.Add(30 * time.Minute)
	if _, err := q.CreateEvent(ctx, gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "E", Description: "d",
		StartsAt: base.Add(-time.Hour), EndsAt: base.Add(time.Hour), FreezeAt: &freezeAt,
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}

	// One team, one solve → a non-trivial board (Alpha = 100).
	chID := uuid.Must(uuid.NewV7())
	if _, err := q.CreateChallenge(ctx, gen.CreateChallengeParams{
		ID: chID, Slug: "c1", Title: "C", Category: "web", Kind: "standard",
		Flag: "OSCTF{f}", Scoring: "static", PointsInitial: 100,
		MemLimitMb: 128, CpuMillis: 500, ContainerEnv: []byte("{}"), Visible: true,
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Username: "au", Email: "a@e.test", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatalf("user: %v", err)
	}
	team := uuid.Must(uuid.NewV7())
	if _, err := q.CreateTeam(ctx, gen.CreateTeamParams{ID: team, Name: "Alpha", InviteCode: "acode", CaptainID: uid}); err != nil {
		t.Fatalf("team: %v", err)
	}
	if err := q.AddTeamMember(ctx, gen.AddTeamMemberParams{TeamID: team, UserID: uid}); err != nil {
		t.Fatalf("member: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO submissions (id, challenge_id, team_id, user_id, provided, correct)
		 VALUES ($1, $2, $3, $4, 'OSCTF{f}', true)`,
		uuid.Must(uuid.NewV7()), chID, team, uid); err != nil {
		t.Fatalf("solve: %v", err)
	}

	ev := events.New(q, clk)
	sb := New(q, rdb, ev, clk)
	if err := sb.Recompute(ctx); err != nil { // warm the cache while Redis is up
		t.Fatalf("recompute: %v", err)
	}

	// --- Phase 1: NOT frozen + Redis paused → DEGRADE (serve a recomputed board, no error) ---
	nowNano.Store(base.UnixNano()) // before FreezeAt → not frozen
	beforeDeg := metrics.CounterValue(metrics.ScoreboardDegradedServed)
	pause()
	snap, err := sb.Current(ctx, false)
	if err != nil {
		unpause()
		t.Fatalf("non-frozen read during a Redis outage must DEGRADE to a Postgres recompute, got error: %v", err)
	}
	if len(snap.Standings) == 0 || snap.Standings[0].Points != 100 {
		unpause()
		t.Fatalf("degraded board is wrong: %+v", snap.Standings)
	}
	if got := metrics.CounterValue(metrics.ScoreboardDegradedServed) - beforeDeg; got < 1 {
		unpause()
		t.Errorf("ScoreboardDegradedServed not incremented on a degraded serve (+%v)", got)
	}

	// --- Phase 2: FROZEN + the SAME outage → FAIL CLOSED (error, never a live board) ---
	nowNano.Store(base.Add(45 * time.Minute).UnixNano()) // past FreezeAt → frozen
	beforeDeg2 := metrics.CounterValue(metrics.ScoreboardDegradedServed)
	if _, ferr := sb.Current(ctx, false); ferr == nil {
		unpause()
		t.Fatal("frozen non-admin read during a Redis outage must FAIL CLOSED — the frozen snapshot lives only in Redis; it must NOT fall through to a live board")
	}
	if got := metrics.CounterValue(metrics.ScoreboardDegradedServed) - beforeDeg2; got != 0 {
		unpause()
		t.Errorf("the freeze path degraded (+%v) — it must never recompute a live board", got)
	}

	// --- Phase 3: recovery — unpause; both paths return to correct behavior without a restart ---
	unpause()
	if snap3, rerr := sb.Current(ctx, false); rerr != nil { // still frozen; Redis back → snapshot re-derived
		t.Fatalf("after Redis recovery the frozen read should serve the re-derived snapshot: %v", rerr)
	} else if !snap3.Frozen || len(snap3.Standings) == 0 || snap3.Standings[0].Points != 100 {
		t.Errorf("post-recovery frozen board wrong: frozen=%v standings=%+v", snap3.Frozen, snap3.Standings)
	}
	nowNano.Store(base.UnixNano()) // not frozen again
	if snap4, rerr := sb.Current(ctx, false); rerr != nil {
		t.Fatalf("after recovery the live read should work normally: %v", rerr)
	} else if len(snap4.Standings) == 0 || snap4.Standings[0].Points != 100 {
		t.Errorf("post-recovery live board wrong: %+v", snap4.Standings)
	}
}
