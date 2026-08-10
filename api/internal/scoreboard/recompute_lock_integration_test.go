//go:build integration

package scoreboard

// Triage of the soak's scoreboard-mismatch (docs/v0.3): Recompute held s.mu across
// compute()'s DB reads, so every recompute serialized behind one in-flight compute.
// Under load (100+ actors, think=0) recomputes queue on the mutex faster than they
// drain, so the served board (keyCurrent) lags the solve log by the queue-drain time
// — a window that crossed the soak's 600ms re-read ~17% of runs and is observable to
// any participant reading /scoreboard (sb.Current(false) IS the REST body). The fix
// runs compute() OUTSIDE the mutex and holds it only for a data-derived guarded write.
//
// These pin: (1) compute() actually runs concurrently, (2) the guard drops an older
// board even when it carries a later clock, and (3) RecomputeForce (which bypasses the
// guard to apply a shrink) does not lose a solve that lands during its compute.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/testsupport"
)

// barrierStore rendezvouses every compute() inside ListValidSolves so the test can
// observe how many run concurrently. ListScoreboardTeams passes straight through; the
// gate is on ListValidSolves (the second, heavier read compute makes).
type barrierStore struct {
	inner   scoreStore
	entered chan struct{}
	release chan struct{}
	cur     atomic.Int32
	max     atomic.Int32
}

func (b *barrierStore) ListScoreboardTeams(ctx context.Context) ([]gen.ListScoreboardTeamsRow, error) {
	return b.inner.ListScoreboardTeams(ctx)
}

func (b *barrierStore) CountValidSolves(ctx context.Context) (int64, error) {
	return b.inner.CountValidSolves(ctx)
}

func (b *barrierStore) ListValidSolves(ctx context.Context) ([]gen.ListValidSolvesRow, error) {
	n := b.cur.Add(1)
	for { // record the high-water mark of concurrent compute()s
		m := b.max.Load()
		if n <= m || b.max.CompareAndSwap(m, n) {
			break
		}
	}
	b.entered <- struct{}{}
	<-b.release
	b.cur.Add(-1)
	return b.inner.ListValidSolves(ctx)
}

func TestRecomputeComputesOutsideLock(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	q := gen.New(pool)
	clk := clock.System()
	ev := events.New(q, clk)
	sb := New(q, rdb, ev, clk)

	const n = 4
	bs := &barrierStore{inner: q, entered: make(chan struct{}, n), release: make(chan struct{})}
	sb.q = bs // inject: compute() now rendezvouses in the barrier (empty DB → empty board)
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(bs.release) }) }
	defer releaseAll() // unblock stragglers even on the timeout (Fatalf) path

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = sb.Recompute(context.Background()) }()
	}

	// All n compute()s must be inside ListValidSolves at once. If compute() runs under
	// s.mu, one holds the lock in the barrier and the other n-1 block on Lock() — the
	// barrier fills to 1 and this times out, which is the pre-fix failure.
	deadline := time.After(4 * time.Second)
	for got := 0; got < n; got++ {
		select {
		case <-bs.entered:
		case <-deadline:
			t.Fatalf("only %d of %d recomputes ran compute() concurrently — compute() is serialized under s.mu, so a slow compute freezes the served board for every queued recompute (the soak's scoreboard-mismatch)", got, n)
		}
	}
	releaseAll()
	wg.Wait()
	if got := bs.max.Load(); got != n {
		t.Fatalf("max concurrent compute()s = %d, want %d", got, n)
	}
}

// boardHarness is a real PG+Redis scoreboard with one 100-pt static challenge, plus
// helpers to add a team, record a raw solve, and read a team's PUBLISHED points.
type boardHarness struct {
	t    *testing.T
	pool *pgxpool.Pool
	q    *gen.Queries
	sb   *Service
	ch   uuid.UUID
}

func newBoardHarness(t *testing.T, base time.Time, clk clock.Clock) *boardHarness {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	q := gen.New(pool)
	ev := events.New(q, clk)
	sb := New(q, rdb, ev, clk)
	ctx := context.Background()
	if _, err := q.CreateEvent(ctx, gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: base.Add(-time.Hour), EndsAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	ch := uuid.Must(uuid.NewV7())
	if _, err := q.CreateChallenge(ctx, gen.CreateChallengeParams{
		ID: ch, Slug: "c1", Title: "C", Category: "web", Kind: "standard",
		Flag: "OSCTF{f}", Scoring: "static", PointsInitial: 100,
		MemLimitMb: 128, CpuMillis: 500, ContainerEnv: []byte("{}"), Visible: true,
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	return &boardHarness{t: t, pool: pool, q: q, sb: sb, ch: ch}
}

func (h *boardHarness) team(name string) uuid.UUID {
	ctx := context.Background()
	uid := uuid.Must(uuid.NewV7())
	if _, err := h.q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Username: name + "u", Email: name + "@e.test", PasswordHash: "x", Role: "user"}); err != nil {
		h.t.Fatalf("user: %v", err)
	}
	tid := uuid.Must(uuid.NewV7())
	if _, err := h.q.CreateTeam(ctx, gen.CreateTeamParams{ID: tid, Name: name, InviteCode: name + "code", CaptainID: uid}); err != nil {
		h.t.Fatalf("team: %v", err)
	}
	if err := h.q.AddTeamMember(ctx, gen.AddTeamMemberParams{TeamID: tid, UserID: uid}); err != nil {
		h.t.Fatalf("member: %v", err)
	}
	return tid
}

func (h *boardHarness) solve(team uuid.UUID) {
	if _, err := h.pool.Exec(context.Background(),
		`INSERT INTO submissions (id, challenge_id, team_id, user_id, provided, correct)
		 SELECT $1, $2, $3, tm.user_id, 'OSCTF{f}', true
		 FROM team_members tm WHERE tm.team_id = $3 LIMIT 1`,
		uuid.Must(uuid.NewV7()), h.ch, team); err != nil {
		h.t.Fatalf("solve: %v", err)
	}
}

func (h *boardHarness) points(team uuid.UUID) int {
	snap, err := h.sb.Current(context.Background(), false)
	if err != nil {
		h.t.Fatalf("current: %v", err)
	}
	for _, e := range snap.Standings {
		if e.TeamID == team {
			return e.Points
		}
	}
	h.t.Fatalf("team %s not in standings", team)
	return -1
}

// blockFirstCompute arms afterComputeHook so the FIRST recompute to reach it pauses after
// its read (signals entered) until release is closed; later recomputes pass through. The
// caller closes release and defers the returned cleanup.
func blockFirstCompute() (entered, release chan struct{}, cleanup func()) {
	entered = make(chan struct{})
	release = make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	afterComputeHook = func() {
		if armed.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
	}
	return entered, release, func() { afterComputeHook = nil }
}

// TestRecomputeGuardKeepsNewerBoardWhenOlderFinishesLast pins the write guard against the
// out-of-order hazard the lock move introduces: two concurrent recomputes, the one that
// read OLDER data forced (via afterComputeHook) to finish LAST, and the newer board must
// survive. Crucially the older-data recompute is handed a LATER clock than the newer one —
// so a guard keyed on a timestamp or entry-sequence would keep the OLDER board (permanent
// divergence). Only a guard keyed on the data itself (the valid-solve count) drops it.
func TestRecomputeGuardKeepsNewerBoardWhenOlderFinishesLast(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	var clockNanos atomic.Int64
	clockNanos.Store(base.UnixNano())
	clk := clock.Clock(func() time.Time { return time.Unix(0, clockNanos.Load()).UTC() })
	h := newBoardHarness(t, base, clk)
	ctx := context.Background()
	alpha, bravo := h.team("Alpha"), h.team("Bravo")

	// Baseline: only Alpha has solved. keyCurrent -> count 1, Alpha=100, Bravo=0.
	h.solve(alpha)
	if err := h.sb.Recompute(ctx); err != nil {
		t.Fatalf("baseline recompute: %v", err)
	}

	entered, release, cleanup := blockFirstCompute()
	defer cleanup()

	// GA reads OLD data (count 1) at a LATE clock, then blocks in the hook.
	clockNanos.Store(base.Add(100 * time.Second).UnixNano())
	gaDone := make(chan error, 1)
	go func() { gaDone <- h.sb.Recompute(ctx) }()
	<-entered // GA has read count=1 and is paused before its write

	// Bravo now solves (count 2). GB reads NEW data at an EARLIER clock and finishes first.
	h.solve(bravo)
	clockNanos.Store(base.Add(50 * time.Second).UnixNano())
	if err := h.sb.Recompute(ctx); err != nil {
		t.Fatalf("GB recompute: %v", err)
	}
	if p := h.points(bravo); p != 100 {
		t.Fatalf("GB (newer board) did not publish: Bravo=%d, want 100", p)
	}

	// GA (older data, LATER clock) resumes and finishes last. The count guard must drop
	// it (count 1 < 2); a timestamp guard would let it overwrite Bravo back to 0.
	close(release)
	if err := <-gaDone; err != nil {
		t.Fatalf("GA recompute: %v", err)
	}
	if p := h.points(bravo); p != 100 {
		t.Fatalf("older recompute clobbered the newer board: Bravo=%d, want 100 — guard is not data-derived", p)
	}
}

// TestCurrentReadRepairServesFreshWhenCacheBehind pins the read-repair invariant by
// construction (no timing): when the cached board is behind the solve log — the durability
// gap where a per-solve recompute never landed — a read must recompute and serve the fresh
// board, never the stale one. This is what makes the soak's served==log invariant true
// structurally rather than by how fast a recompute happens to run.
func TestCurrentReadRepairServesFreshWhenCacheBehind(t *testing.T) {
	h := newBoardHarness(t, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), clock.System())
	ctx := context.Background()
	alpha, bravo := h.team("Alpha"), h.team("Bravo")

	// Baseline: only Alpha solved, and it's in the cache (count 1).
	h.solve(alpha)
	if err := h.sb.Recompute(ctx); err != nil {
		t.Fatalf("baseline recompute: %v", err)
	}

	// Bravo solves, but NOTHING recomputes — the cache is now stale (log has 2, cache 1).
	h.solve(bravo)

	// A read MUST serve fresh: read-repair detects the log moved past the cached SolveCount
	// and recomputes before returning.
	snap, err := h.sb.Current(ctx, false)
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	if snap.SolveCount != 2 {
		t.Fatalf("served board SolveCount=%d, want 2 — read-repair did not recompute the stale cache", snap.SolveCount)
	}
	got := 0
	for _, e := range snap.Standings {
		if e.TeamID == bravo {
			got = e.Points
		}
	}
	if got != 100 {
		t.Fatalf("read-repair served a stale board: Bravo=%d, want 100", got)
	}
}

// TestForceRecomputeDoesNotLoseConcurrentSolve pins the RecomputeForce two-step. A forced
// recompute (an admin hiding/deleting a challenge) bypasses the count guard to land the
// shrink, so its unconditional write can clobber a solve that commits during its compute.
// The immediately-following GUARDED re-run must pick that solve back up — otherwise, since
// production has no periodic recompute (the 15s ticker recomputes only on a phase change),
// the board would miss the solve until the next solve, which is not guaranteed to follow.
func TestForceRecomputeDoesNotLoseConcurrentSolve(t *testing.T) {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	h := newBoardHarness(t, base, clock.System())
	ctx := context.Background()
	alpha, bravo := h.team("Alpha"), h.team("Bravo")

	// Baseline: only Alpha has solved. keyCurrent -> count 1.
	h.solve(alpha)
	if err := h.sb.Recompute(ctx); err != nil {
		t.Fatalf("baseline recompute: %v", err)
	}

	entered, release, cleanup := blockFirstCompute()
	defer cleanup()

	// RecomputeForce reads count 1 in its force step, then blocks before its write.
	forceDone := make(chan error, 1)
	go func() { forceDone <- h.sb.RecomputeForce(ctx) }()
	<-entered

	// Bravo solves while the force step is mid-compute (count -> 2). Nothing else
	// recomputes — only RecomputeForce's own guarded re-run can recover Bravo.
	h.solve(bravo)

	// Release: the force step writes board(count 1) unconditionally (clobbering Bravo),
	// then the guarded re-run reads count 2 and restores Bravo.
	close(release)
	if err := <-forceDone; err != nil {
		t.Fatalf("RecomputeForce: %v", err)
	}
	if p := h.points(bravo); p != 100 {
		t.Fatalf("a solve landing during a forced recompute was lost: Bravo=%d, want 100 (force must re-run guarded)", p)
	}
}
