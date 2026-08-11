//go:build integration

package submissions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/testsupport"
)

// --- test doubles for the challenge-type resolver ---

type fakeResolver struct {
	plugin  map[string]submissions.FlagChecker // plugin types (isPlugin=true, registered=true)
	builtin map[string]bool                    // built-in types (isPlugin=false, registered=true)
}

func (f fakeResolver) Resolve(id string) (submissions.FlagChecker, bool, bool) {
	if c, ok := f.plugin[id]; ok {
		return c, true, true
	}
	if f.builtin[id] {
		return nil, false, true
	}
	return nil, false, false // unregistered → reject-retry
}

type constChecker struct {
	correct bool
	err     error
}

func (c constChecker) CheckFlag(context.Context, string, map[string]string, map[string]string) (bool, error) {
	return c.correct, c.err
}

// mutatingChecker runs a side effect (edit/delete the challenge) DURING the verdict, to simulate a
// swap/delete landing between the pre-tx verdict and the in-tx lock.
type mutatingChecker struct {
	correct bool
	mutate  func()
}

func (m mutatingChecker) CheckFlag(context.Context, string, map[string]string, map[string]string) (bool, error) {
	if m.mutate != nil {
		m.mutate()
	}
	return m.correct, nil
}

// --- helpers ---

func seedTypedChallenge(t *testing.T, pool *pgxpool.Pool, q *gen.Queries, typ string, maxAttempts *int32) (uuid.UUID, string) {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	slug := "ty-" + uniq(id)
	if _, err := q.CreateChallenge(context.Background(), gen.CreateChallengeParams{
		ID: id, Slug: slug, Title: "T", Category: "misc", Kind: "standard",
		Flag: "OSCTF{unused_for_plugin}", Scoring: "static", PointsInitial: 100,
		MemLimitMb: 256, CpuMillis: 500, ContainerEnv: []byte("{}"), Visible: true,
		MaxAttempts: maxAttempts,
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE challenges SET type=$2 WHERE id=$1`, id, typ); err != nil {
		t.Fatalf("set type: %v", err)
	}
	return id, slug
}

func rowCount(t *testing.T, pool *pgxpool.Pool, team, ch uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM submissions WHERE team_id=$1 AND challenge_id=$2`, team, ch).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func i32(v int32) *int32 { return &v }

// A plugin type whose checker is unavailable (unregistered / call error) is REJECT-RETRY: a
// distinct 503-mapped error, and NO attempt is recorded — never shown as "incorrect".
func TestPluginRejectRetryConsumesNoAttempt(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	for _, tc := range []struct {
		name     string
		resolver submissions.ChallengeTypes
	}{
		{"unregistered", fakeResolver{}}, // "crypto" not in the resolver → registered=false
		{"call-errors", fakeResolver{plugin: map[string]submissions.FlagChecker{"crypto": constChecker{err: errors.New("plugin down")}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chID, slug := seedTypedChallenge(t, pool, q, "crypto", nil)
			team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
			user := captainOf(t, q, team)
			svc := newSubmissionsService(t, pool).WithChallengeTypes(tc.resolver)

			_, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "guess"})
			var unavail *apperr.Unavailable
			if !errors.As(err, &unavail) {
				t.Fatalf("reject-retry err = %v; want *apperr.Unavailable (503), distinct from a wrong answer", err)
			}
			if n := rowCount(t, pool, team, chID); n != 0 {
				t.Errorf("reject-retry recorded %d attempts; want 0 (no attempt consumed)", n)
			}
		})
	}
}

// A plugin verdict of CORRECT records a solve; WRONG records an attempt (correct=false). The
// plugin owns correctness; the platform records it.
func TestPluginVerdictSolveAndAttempt(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	t.Run("correct-solves", func(t *testing.T) {
		chID, slug := seedTypedChallenge(t, pool, q, "crypto", nil)
		team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
		user := captainOf(t, q, team)
		svc := newSubmissionsService(t, pool).WithChallengeTypes(
			fakeResolver{plugin: map[string]submissions.FlagChecker{"crypto": constChecker{correct: true}}})

		res, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "right"})
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		if !res.Correct {
			t.Error("plugin verdict correct=true did not solve")
		}
		var correct bool
		_ = pool.QueryRow(ctx, `SELECT correct FROM submissions WHERE team_id=$1 AND challenge_id=$2`, team, chID).Scan(&correct)
		if !correct {
			t.Error("solve row not recorded correct=true")
		}
	})

	t.Run("wrong-consumes-attempt", func(t *testing.T) {
		chID, slug := seedTypedChallenge(t, pool, q, "crypto", nil)
		team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
		user := captainOf(t, q, team)
		svc := newSubmissionsService(t, pool).WithChallengeTypes(
			fakeResolver{plugin: map[string]submissions.FlagChecker{"crypto": constChecker{correct: false}}})

		res, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "wrong"})
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		if res.Correct {
			t.Error("plugin verdict correct=false was recorded as a solve")
		}
		if n := rowCount(t, pool, team, chID); n != 1 {
			t.Errorf("wrong answer recorded %d attempts; want 1", n)
		}
	})
}

// A challenge SWAPPED (type changed) or DELETED between the pre-tx verdict and the in-tx lock
// fails closed as reject-retry — the verdict against the old row is void — and consumes no attempt.
func TestPluginSwapOrDeleteFailsClosed(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	t.Run("type-swapped", func(t *testing.T) {
		chID, slug := seedTypedChallenge(t, pool, q, "crypto", nil)
		team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
		user := captainOf(t, q, team)
		swap := func() { _, _ = pool.Exec(ctx, `UPDATE challenges SET type='other' WHERE id=$1`, chID) }
		svc := newSubmissionsService(t, pool).WithChallengeTypes(
			fakeResolver{plugin: map[string]submissions.FlagChecker{"crypto": mutatingChecker{correct: true, mutate: swap}}})

		_, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "x"})
		var unavail *apperr.Unavailable
		if !errors.As(err, &unavail) {
			t.Fatalf("swapped-type err = %v; want reject-retry (verdict is void)", err)
		}
		if n := rowCount(t, pool, team, chID); n != 0 {
			t.Errorf("swapped-type recorded %d attempts; want 0", n)
		}
	})

	t.Run("deleted", func(t *testing.T) {
		chID, slug := seedTypedChallenge(t, pool, q, "crypto", nil)
		team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
		user := captainOf(t, q, team)
		del := func() { _, _ = pool.Exec(ctx, `DELETE FROM challenges WHERE id=$1`, chID) }
		svc := newSubmissionsService(t, pool).WithChallengeTypes(
			fakeResolver{plugin: map[string]submissions.FlagChecker{"crypto": mutatingChecker{correct: true, mutate: del}}})

		_, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "x"})
		var unavail *apperr.Unavailable
		if !errors.As(err, &unavail) {
			t.Fatalf("deleted-challenge err = %v; want reject-retry (missing row)", err)
		}
	})
}

// ORDERING (load-bearing): the deleted/swapped check MUST precede the attempt and solve checks, so
// a swapped-or-deleted challenge fails closed WITHOUT consuming an attempt. Setup: a plugin
// challenge at its attempt cap whose verdict swaps the type. Correct order → reject-retry
// (Unavailable). If the attempt check ran first, the team is at the cap → Forbidden. Asserting
// Unavailable (not Forbidden) fails if the checks are reordered.
func TestPluginSwapCheckPrecedesAttemptCheck(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	chID, slug := seedTypedChallenge(t, pool, q, "crypto", i32(1)) // max_attempts = 1
	team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
	user := captainOf(t, q, team)

	// One prior wrong attempt → the team is AT the cap.
	if _, err := q.CreateSubmission(ctx, gen.CreateSubmissionParams{
		ID: uuid.Must(uuid.NewV7()), ChallengeID: chID, TeamID: team, UserID: user, Provided: "prev", Correct: false,
	}); err != nil {
		t.Fatalf("seed prior attempt: %v", err)
	}

	swap := func() { _, _ = pool.Exec(ctx, `UPDATE challenges SET type='other' WHERE id=$1`, chID) }
	svc := newSubmissionsService(t, pool).WithChallengeTypes(
		fakeResolver{plugin: map[string]submissions.FlagChecker{"crypto": mutatingChecker{correct: true, mutate: swap}}})

	_, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "x"})
	var unavail *apperr.Unavailable
	var forbidden *apperr.Forbidden
	if errors.As(err, &forbidden) {
		t.Fatalf("got Forbidden — the attempt check ran BEFORE the swap check; a swapped challenge must fail closed as reject-retry first")
	}
	if !errors.As(err, &unavail) {
		t.Fatalf("swap+at-cap err = %v; want *apperr.Unavailable", err)
	}
	if n := rowCount(t, pool, team, chID); n != 1 {
		t.Errorf("recorded %d attempts; want 1 (the swap consumed none)", n)
	}
}

// The regression gate at the unit level: with the resolver wired but the type resolving to a
// BUILT-IN, the submission path uses the platform's flag comparison exactly as v0.2 — the plugin
// verdict path is not taken.
func TestBuiltinTypeWithResolverUnchanged(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	chID, slug := seedTypedChallenge(t, pool, q, "standard", nil)
	// overwrite the flag to a known value
	if _, err := pool.Exec(ctx, `UPDATE challenges SET flag='OSCTF{builtin}' WHERE id=$1`, chID); err != nil {
		t.Fatal(err)
	}
	team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
	user := captainOf(t, q, team)
	svc := newSubmissionsService(t, pool).WithChallengeTypes(fakeResolver{builtin: map[string]bool{"standard": true}})

	// Correct flag → solved via the built-in compare (not a plugin verdict).
	res, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "OSCTF{builtin}"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if !res.Correct {
		t.Error("built-in type with resolver wired did not accept the correct flag (v0.2 path changed)")
	}
}
