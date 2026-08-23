//go:build integration && crashtest

package submissions_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/swayyaam/OSCTF/internal/audit"
	"github.com/swayyaam/OSCTF/internal/clock"
	"github.com/swayyaam/OSCTF/internal/db"
	"github.com/swayyaam/OSCTF/internal/db/gen"
	"github.com/swayyaam/OSCTF/internal/events"
	"github.com/swayyaam/OSCTF/internal/submissions"
	"github.com/swayyaam/OSCTF/internal/testsupport"
)

// crashSeamExitCode is the exit code the child uses at the commit→async seam, so the parent can tell
// "crashed exactly there" from a clean finish (0) or any other failure.
const crashSeamExitCode = 87

// TestCrashBetweenCommitAndAsyncRecovers ACTUALLY KILLS the process at the instant between a
// submission's commit and its post-commit async work, and proves the durability gap is recovered.
// A child subprocess submits a correct solve on a plugin-scored challenge, then os.Exits at
// afterCommitCrashHook — after the solve COMMITS, before the scoring record is written. The parent
// then asserts the solve is durable, the record is genuinely MISSING (so the "committed-but-async-
// skipped" state the repair worker targets is exactly what a real crash produces — no partial write,
// no in-memory bookkeeping), and the repair worker backfills it. Complements
// TestScoreRepairBackfillsMissingRecord, which builds that state directly; here the crash proves the
// built state is the crash's state.
func TestCrashBetweenCommitAndAsyncRecovers(t *testing.T) {
	if dsn := os.Getenv("OSCTF_CRASHCHILD_DSN"); dsn != "" {
		crashChild(dsn) // runs inside the child subprocess; never returns (os.Exit)
		return
	}

	// ---- PARENT ----
	pool, dsn := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	now := time.Now().UTC()
	if _, err := q.CreateEvent(ctx, gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	chID, slug := seedScoredChallenge(t, pool, q, "custom", "OSCTF{crash}")
	team := seedTeamWithInstance(t, pool, q, chID, 0, nil)
	user := captainOf(t, q, team)

	// Spawn ourselves as the child, pointed at this DB + the seeded ids. It commits a solve and dies
	// at the seam.
	cmd := exec.Command(os.Args[0], "-test.run", "^TestCrashBetweenCommitAndAsyncRecovers$", "-test.v") //nolint:gosec // G204: re-executing our own test binary with a fixed -test.run filter.
	cmd.Env = append(os.Environ(),
		"OSCTF_CRASHCHILD_DSN="+dsn,
		"OSCTF_CRASHCHILD_SLUG="+slug,
		"OSCTF_CRASHCHILD_TEAM="+team.String(),
		"OSCTF_CRASHCHILD_USER="+user.String(),
	)
	out, err := cmd.CombinedOutput()

	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != crashSeamExitCode {
		t.Fatalf("child did not crash at the commit→async seam (want exit %d): err=%v\n--- child output ---\n%s",
			crashSeamExitCode, err, out)
	}

	// 1. The solve is DURABLE despite the crash — commit landed before the process died.
	var correct bool
	var scoredBy *string
	if err := pool.QueryRow(ctx,
		`SELECT correct, scored_by FROM submissions WHERE team_id=$1 AND challenge_id=$2`, team, chID).
		Scan(&correct, &scoredBy); err != nil {
		t.Fatalf("the committed solve is gone — commit did not durably land before the crash: %v", err)
	}
	if !correct {
		t.Fatal("the solve row is not correct=true")
	}
	// 2. The scoring record is MISSING — the crash produced exactly the durability-gap state.
	if scoredBy != nil {
		t.Fatalf("scored_by=%q — the post-commit record ran, so the child did not die at the seam", *scoredBy)
	}
	// 3. The repair worker RECOVERS it.
	repairer := submissions.NewScoreRepairer(pool, &fakeScorer{value: 250, by: "custom", hasValue: true})
	if _, rerr := repairer.RepairOnce(ctx); rerr != nil {
		t.Fatalf("repair pass: %v", rerr)
	}
	scoredBy = nil
	_ = pool.QueryRow(ctx,
		`SELECT scored_by FROM submissions WHERE team_id=$1 AND challenge_id=$2`, team, chID).Scan(&scoredBy)
	if scoredBy == nil || *scoredBy != "custom" {
		t.Fatalf("after the repair pass scored_by=%v, want \"custom\" — the repair worker did not recover the crashed solve's record", scoredBy)
	}
}

// crashChild runs in the child subprocess: connect to the parent's DB, submit the correct flag, and
// os.Exit at the post-commit seam. It never returns.
func crashChild(dsn string) {
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn, testsupport.DiscardLogger())
	if err != nil {
		fmt.Fprintln(os.Stderr, "child: connect:", err)
		os.Exit(2)
	}
	q := gen.New(pool)
	clk := clock.System()
	svc := submissions.New(pool, events.New(q, clk), clk, audit.New(q, testsupport.DiscardLogger())).
		WithScorer(&fakeScorer{value: 250, by: "custom", hasValue: true})

	// Arm the crash: os.Exit the instant after commit, before the scoring record is written.
	submissions.SetAfterCommitCrashHookForTest(func() { os.Exit(crashSeamExitCode) })

	_, serr := svc.Submit(ctx, submissions.Input{
		UserID: uuid.MustParse(os.Getenv("OSCTF_CRASHCHILD_USER")),
		TeamID: uuid.MustParse(os.Getenv("OSCTF_CRASHCHILD_TEAM")),
		Slug:   os.Getenv("OSCTF_CRASHCHILD_SLUG"),
		Flag:   "OSCTF{crash}",
	})
	// Reaching here means the seam did NOT fire — Submit returned. Fail loudly with a distinct code.
	fmt.Fprintln(os.Stderr, "child: Submit returned without crashing at the seam; err =", serr)
	os.Exit(3)
}
