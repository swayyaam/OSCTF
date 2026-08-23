//go:build integration

package submissions_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/swayyaam/OSCTF/internal/db/gen"
	"github.com/swayyaam/OSCTF/internal/metrics"
	"github.com/swayyaam/OSCTF/internal/submissions"
	"github.com/swayyaam/OSCTF/internal/testsupport"
)

// seedCorrectSolveNoRecord inserts a CORRECT solve with no scoring columns set — exactly the state
// a post-commit record-write failure leaves behind: a MISSING record.
func seedCorrectSolveNoRecord(t *testing.T, q *gen.Queries, chID, team, user uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := q.CreateSubmission(context.Background(), gen.CreateSubmissionParams{
		ID: id, ChallengeID: chID, TeamID: team, UserID: user, Provided: "x", Correct: true,
	}); err != nil {
		t.Fatalf("seed correct solve: %v", err)
	}
	return id
}

// scoreRecordByID reads (scored_value, scored_by) for a specific submission row.
func scoreRecordByID(t *testing.T, pool *pgxpool.Pool, id uuid.UUID) (*int32, *string) {
	t.Helper()
	var sv *int32
	var sb *string
	if err := pool.QueryRow(context.Background(),
		`SELECT scored_value, scored_by FROM submissions WHERE id=$1`, id).Scan(&sv, &sb); err != nil {
		t.Fatalf("read record: %v", err)
	}
	return sv, sb
}

// The repair worker backfills a MISSING record off the read path: the value the plugin now returns
// is recorded, and the record stops being missing.
func TestScoreRepairBackfillsMissingRecord(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	chID, _ := seedScoredChallenge(t, pool, q, "custom", "OSCTF{r}")
	team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
	user := captainOf(t, q, team)
	solveID := seedCorrectSolveNoRecord(t, q, chID, team, user)

	repairer := submissions.NewScoreRepairer(pool, &fakeScorer{value: 300, by: "custom", hasValue: true})
	filled, err := repairer.RepairOnce(ctx)
	if err != nil {
		t.Fatalf("RepairOnce: %v", err)
	}
	if filled != 1 {
		t.Errorf("filled = %d, want 1", filled)
	}
	sv, sb := scoreRecordByID(t, pool, solveID)
	if sv == nil || *sv != 300 || sb == nil || *sb != "custom" {
		t.Errorf("record = (%v, %v), want (300, custom)", sv, sb)
	}
}

// When the scorer defers (plugin down, no fallback), the worker records 'pending' — a MISSING
// record becomes a PENDING one, so every valid plugin solve carries a record. It never leaves the
// row a bare absence.
func TestScoreRepairRecordsPendingWhenDeferred(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	chID, _ := seedScoredChallenge(t, pool, q, "custom", "OSCTF{p}")
	team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
	user := captainOf(t, q, team)
	solveID := seedCorrectSolveNoRecord(t, q, chID, team, user)

	repairer := submissions.NewScoreRepairer(pool, &fakeScorer{by: "pending", hasValue: false})
	filled, err := repairer.RepairOnce(ctx)
	if err != nil {
		t.Fatalf("RepairOnce: %v", err)
	}
	if filled != 0 {
		t.Errorf("filled = %d, want 0 (deferred, not valued)", filled)
	}
	sv, sb := scoreRecordByID(t, pool, solveID)
	if sv != nil {
		t.Errorf("scored_value = %v, want NULL (pending)", *sv)
	}
	if sb == nil || *sb != "pending" {
		t.Errorf("scored_by = %v, want pending", sb)
	}
}

// The worker touches ONLY missing/pending plugin solves: a built-in solve and an already-resolved
// plugin solve are never handed to the scorer and never rewritten.
func TestScoreRepairSkipsBuiltinAndResolved(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	// A built-in solve (must never be scored by the plugin path).
	biCh, _ := seedScoredChallenge(t, pool, q, "static", "OSCTF{bi}")
	biTeam := seedTeamWithInstance(t, pool, q, biCh, 0, nil)
	seedCorrectSolveNoRecord(t, q, biCh, biTeam, captainOf(t, q, biTeam))

	// A plugin solve already resolved (has a value) — outside the needing-score set.
	resCh, _ := seedScoredChallenge(t, pool, q, "custom", "OSCTF{res}")
	resTeam := seedTeamWithInstance(t, pool, q, resCh, 0, nil)
	resSolve := seedCorrectSolveNoRecord(t, q, resCh, resTeam, captainOf(t, q, resTeam))
	if _, err := pool.Exec(ctx, `UPDATE submissions SET scored_value=42, scored_by='custom' WHERE id=$1`, resSolve); err != nil {
		t.Fatal(err)
	}

	sc := &fakeScorer{value: 999, by: "custom", hasValue: true}
	repairer := submissions.NewScoreRepairer(pool, sc)
	filled, err := repairer.RepairOnce(ctx)
	if err != nil {
		t.Fatalf("RepairOnce: %v", err)
	}
	if filled != 0 {
		t.Errorf("filled = %d, want 0 (nothing needs scoring)", filled)
	}
	if sc.calls != 0 {
		t.Errorf("scorer consulted %d times; want 0 (built-in + resolved are out of scope)", sc.calls)
	}
	// The resolved record is unchanged.
	sv, _ := scoreRecordByID(t, pool, resSolve)
	if sv == nil || *sv != 42 {
		t.Errorf("resolved record changed to %v; want 42 untouched", sv)
	}
}

// A nil scorer refreshes the missing/pending gauges (the durability signal) and backfills nothing.
// Missing (no record at all) is counted SEPARATELY from pending (a deferred record).
func TestScoreRepairGaugesCountMissingSeparatelyFromPending(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	ch, _ := seedScoredChallenge(t, pool, q, "custom", "OSCTF{g}")
	// two MISSING solves (different teams, same challenge) ...
	for i := 0; i < 2; i++ {
		tm := seedTeamWithInstance(t, pool, q, ch, 0, nil)
		seedCorrectSolveNoRecord(t, q, ch, tm, captainOf(t, q, tm))
	}
	// ... and one PENDING solve.
	pt := seedTeamWithInstance(t, pool, q, ch, 0, nil)
	pending := seedCorrectSolveNoRecord(t, q, ch, pt, captainOf(t, q, pt))
	if _, err := pool.Exec(ctx, `UPDATE submissions SET scored_by='pending' WHERE id=$1`, pending); err != nil {
		t.Fatal(err)
	}

	repairer := submissions.NewScoreRepairer(pool, nil) // gauge refresh only
	if _, err := repairer.RepairOnce(ctx); err != nil {
		t.Fatalf("RepairOnce: %v", err)
	}
	if got := metrics.GaugeValue(metrics.PluginScoresMissing); got != 2 {
		t.Errorf("missing gauge = %v, want 2", got)
	}
	if got := metrics.GaugeValue(metrics.PluginScoresPending); got != 1 {
		t.Errorf("pending gauge = %v, want 1", got)
	}
}
