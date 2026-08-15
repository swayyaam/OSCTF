//go:build integration

package submissions_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/scoring"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/testsupport"
)

// fakeScorer stands in for the plugin scorer. It records what it was asked and returns a canned
// verdict — no subprocess, so the record/read/repair mechanism is exercised deterministically.
type fakeScorer struct {
	value     int
	by        string
	hasValue  bool
	calls     int
	gotMode   string
	gotSolves int
}

func (f *fakeScorer) Score(_ context.Context, mode string, _ scoring.ChallengeScoring, solves int) (int, string, bool) {
	f.calls++
	f.gotMode = mode
	f.gotSolves = solves
	return f.value, f.by, f.hasValue
}

// seedScoredChallenge creates a built-in-type (standard flag compare) challenge with a PLUGIN
// scoring mode, so the flag path is ordinary and only the scoring is plugin-driven. It writes
// scoring=mode directly (0007 dropped the DB CHECK; app validation is tested elsewhere).
func seedScoredChallenge(t *testing.T, pool *pgxpool.Pool, q *gen.Queries, mode, flag string) (uuid.UUID, string) {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	slug := "sc-" + uniq(id)
	if _, err := q.CreateChallenge(context.Background(), gen.CreateChallengeParams{
		ID: id, Slug: slug, Title: "S", Category: "misc", Kind: "standard",
		Flag: flag, Scoring: mode, PointsInitial: 500,
		MemLimitMb: 256, CpuMillis: 500, ContainerEnv: []byte("{}"), Visible: true,
	}); err != nil {
		t.Fatalf("create scored challenge: %v", err)
	}
	return id, slug
}

// scoreRecord reads the (scored_value, scored_by) recorded on a team's correct solve of ch.
func scoreRecord(t *testing.T, pool *pgxpool.Pool, team, ch uuid.UUID) (*int32, *string) {
	t.Helper()
	var sv *int32
	var sb *string
	if err := pool.QueryRow(context.Background(),
		`SELECT scored_value, scored_by FROM submissions WHERE team_id=$1 AND challenge_id=$2 AND correct`,
		team, ch).Scan(&sv, &sb); err != nil {
		t.Fatalf("read score record: %v", err)
	}
	return sv, sb
}

// A plugin-scored solve records the LOCKED value on the submission row, and the participant is
// shown that value — computed once, post-commit, from the plugin.
func TestPluginScoreRecordedOnSolve(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	chID, slug := seedScoredChallenge(t, pool, q, "custom", "OSCTF{scored}")
	team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
	user := captainOf(t, q, team)
	sc := &fakeScorer{value: 250, by: "custom", hasValue: true}
	svc := newSubmissionsService(t, pool).WithScorer(sc)

	res, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "OSCTF{scored}"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !res.Correct {
		t.Fatal("correct flag not accepted")
	}
	if res.Points == nil || *res.Points != 250 {
		t.Errorf("res.Points = %v, want 250 (the recorded plugin value)", res.Points)
	}
	if res.Pending {
		t.Error("res.Pending = true; want false (the plugin produced a value)")
	}
	if sc.calls != 1 {
		t.Errorf("scorer called %d times; want 1", sc.calls)
	}
	if sc.gotMode != "custom" || sc.gotSolves != 1 {
		t.Errorf("scorer got (mode=%q, solves=%d); want (custom, 1)", sc.gotMode, sc.gotSolves)
	}
	sv, sb := scoreRecord(t, pool, team, chID)
	if sv == nil || *sv != 250 {
		t.Errorf("scored_value = %v, want 250", sv)
	}
	if sb == nil || *sb != "custom" {
		t.Errorf("scored_by = %v, want \"custom\"", sb)
	}
}

// Fallback-on with the plugin down: the record carries the fallback value labelled 'fallback', and
// the participant sees that value (no pending).
func TestPluginScoreFallbackRecorded(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	chID, slug := seedScoredChallenge(t, pool, q, "custom", "OSCTF{fb}")
	team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
	user := captainOf(t, q, team)
	sc := &fakeScorer{value: 500, by: "fallback", hasValue: true}
	svc := newSubmissionsService(t, pool).WithScorer(sc)

	res, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "OSCTF{fb}"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Points == nil || *res.Points != 500 || res.Pending {
		t.Errorf("res = {Points:%v Pending:%v}, want {500 false}", res.Points, res.Pending)
	}
	sv, sb := scoreRecord(t, pool, team, chID)
	if sv == nil || *sv != 500 || sb == nil || *sb != "fallback" {
		t.Errorf("record = (%v, %v), want (500, fallback)", sv, sb)
	}
}

// Plugin down + no fallback: the record is 'pending' with NULL value (resolves to 0 on the board),
// and the participant is told the score is pending rather than shown a bare 0.
func TestPluginScorePendingWhenDeferred(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	chID, slug := seedScoredChallenge(t, pool, q, "custom", "OSCTF{pend}")
	team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
	user := captainOf(t, q, team)
	sc := &fakeScorer{by: "pending", hasValue: false}
	svc := newSubmissionsService(t, pool).WithScorer(sc)

	res, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "OSCTF{pend}"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Points == nil || *res.Points != 0 {
		t.Errorf("res.Points = %v, want 0 (deferred)", res.Points)
	}
	if !res.Pending {
		t.Error("res.Pending = false; want true (value deferred)")
	}
	sv, sb := scoreRecord(t, pool, team, chID)
	if sv != nil {
		t.Errorf("scored_value = %v, want NULL (pending)", *sv)
	}
	if sb == nil || *sb != "pending" {
		t.Errorf("scored_by = %v, want \"pending\"", sb)
	}
}

// Regression: a BUILT-IN static/dynamic challenge writes NO scoring record even with a scorer
// wired, and its value is the formula — byte-identical to v0.2. A wrong attempt on a plugin-scored
// challenge likewise records nothing (only correct solves are scored).
func TestBuiltinScoringWritesNoRecord(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	chID, slug := seedScoredChallenge(t, pool, q, "static", "OSCTF{builtin}")
	team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
	user := captainOf(t, q, team)
	sc := &fakeScorer{value: 999, by: "custom", hasValue: true} // must NOT be consulted
	svc := newSubmissionsService(t, pool).WithScorer(sc)

	res, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "OSCTF{builtin}"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if res.Points == nil || *res.Points != 500 { // static → PointsInitial
		t.Errorf("res.Points = %v, want 500 (static formula, not the scorer)", res.Points)
	}
	if sc.calls != 0 {
		t.Errorf("scorer consulted %d times for a built-in mode; want 0", sc.calls)
	}
	sv, sb := scoreRecord(t, pool, team, chID)
	if sv != nil || sb != nil {
		t.Errorf("built-in solve recorded (scored_value=%v, scored_by=%v); want both NULL", sv, sb)
	}
}
