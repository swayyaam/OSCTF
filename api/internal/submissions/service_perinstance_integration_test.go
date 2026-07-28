package submissions_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/testsupport"
)

func uniq(id uuid.UUID) string { return strings.ReplaceAll(id.String(), "-", "")[20:] }

func newSubmissionsService(t *testing.T, pool *pgxpool.Pool) *submissions.Service {
	t.Helper()
	q := gen.New(pool)
	now := time.Now().UTC()
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	clk := clock.System()
	return submissions.New(pool, events.New(q, clk), clk, audit.New(q, testsupport.DiscardLogger()))
}

func seedPerInstanceChallenge(t *testing.T, pool *pgxpool.Pool, q *gen.Queries) (uuid.UUID, string) {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	slug := "pi-" + uniq(id)
	img := "x/y:latest"
	port := int32(8000)
	if _, err := q.CreateChallenge(context.Background(), gen.CreateChallengeParams{
		ID: id, Slug: slug, Title: "PI", Category: "web", Kind: "container",
		Flag: "OSCTF{static_placeholder}", Scoring: "static", PointsInitial: 100,
		Image: &img, InternalPort: &port, MemLimitMb: 128, CpuMillis: 500,
		ContainerEnv: []byte("{}"), Visible: true,
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE challenges SET instancing='per_team', flag_mode='per_instance' WHERE id=$1`, id); err != nil {
		t.Fatalf("set per_instance: %v", err)
	}
	return id, slug
}

func seedTeamWithInstance(t *testing.T, pool *pgxpool.Pool, q *gen.Queries, chID uuid.UUID, port int32, flag *string) uuid.UUID {
	t.Helper()
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(context.Background(), gen.CreateUserParams{
		ID: uid, Username: "u" + uniq(uid), Email: uniq(uid) + "@e.test", PasswordHash: "x", Role: "user",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	tid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateTeam(context.Background(), gen.CreateTeamParams{
		ID: tid, Name: "t" + uniq(tid), InviteCode: uniq(tid), CaptainID: uid,
	}); err != nil {
		t.Fatalf("create team: %v", err)
	}
	if flag != nil {
		hp := port
		if _, err := q.CreateInstance(context.Background(), gen.CreateInstanceParams{
			ID: uuid.Must(uuid.NewV7()), ChallengeID: chID, TeamID: &tid, State: "running",
			HostPort: &hp, Flag: flag, Network: strptr("osctf-team-" + uniq(tid)),
		}); err != nil {
			t.Fatalf("create instance: %v", err)
		}
	}
	return tid
}

func strptr(s string) *string { return &s }

// TestPerInstanceSubmissionIntegration covers the per_instance flag path:
// own flag solves, another team's flag is wrong AND raises a sharing signal,
// and a missing instance is a 403 (not a burned attempt).
func TestPerInstanceSubmissionIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	svc := newSubmissionsService(t, pool)
	ctx := context.Background()

	chID, slug := seedPerInstanceChallenge(t, pool, q)
	flagA := "osctf{team_a_flag}"
	flagB := "osctf{team_b_flag}"
	teamA := seedTeamWithInstance(t, pool, q, chID, 30901, &flagA)
	teamB := seedTeamWithInstance(t, pool, q, chID, 30902, &flagB)
	userA := captainOf(t, q, teamA)
	userB := captainOf(t, q, teamB)

	// Team B submits team A's flag: wrong for B, and a sharing signal is raised.
	res, err := svc.Submit(ctx, submissions.Input{
		UserID: userB, TeamID: teamB, Slug: slug, Flag: flagA,
	})
	if err != nil {
		t.Fatalf("submit B->A flag: %v", err)
	}
	if res.Correct {
		t.Error("team B solved with team A's flag (should be incorrect)")
	}
	assertSharingSignal(t, pool, teamB, teamA, chID)

	// Team A submits its own flag: correct.
	res, err = svc.Submit(ctx, submissions.Input{UserID: userA, TeamID: teamA, Slug: slug, Flag: flagA})
	if err != nil {
		t.Fatalf("submit A->A flag: %v", err)
	}
	if !res.Correct {
		t.Error("team A could not solve with its own flag")
	}

	// A team with no instance gets a 403 (no attempt recorded).
	teamC := seedTeamWithInstance(t, pool, q, chID, 0, nil) // nil flag => no instance
	userC := captainOf(t, q, teamC)
	_, err = svc.Submit(ctx, submissions.Input{UserID: userC, TeamID: teamC, Slug: slug, Flag: flagA})
	var forbidden *apperr.Forbidden
	if !errors.As(err, &forbidden) {
		t.Fatalf("no-instance submit err = %v, want Forbidden", err)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM submissions WHERE team_id=$1`, teamC).Scan(&n)
	if n != 0 {
		t.Errorf("no-instance submit recorded %d attempts, want 0", n)
	}
}

func TestStaticSubmissionUnchangedIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	svc := newSubmissionsService(t, pool)
	ctx := context.Background()

	id := uuid.Must(uuid.NewV7())
	slug := "st-" + uniq(id)
	if _, err := q.CreateChallenge(ctx, gen.CreateChallengeParams{
		ID: id, Slug: slug, Title: "S", Category: "misc", Kind: "standard",
		Flag: "OSCTF{static_win}", Scoring: "static", PointsInitial: 100,
		MemLimitMb: 256, CpuMillis: 500, ContainerEnv: []byte("{}"), Visible: true,
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	team := seedTeamWithInstance(t, pool, q, id, 0, nil)
	user := captainOf(t, q, team)

	res, err := svc.Submit(ctx, submissions.Input{UserID: user, TeamID: team, Slug: slug, Flag: "OSCTF{static_win}"})
	if err != nil {
		t.Fatalf("static submit: %v", err)
	}
	if !res.Correct {
		t.Error("static challenge did not accept the correct flag")
	}
}

func captainOf(t *testing.T, q *gen.Queries, teamID uuid.UUID) uuid.UUID {
	t.Helper()
	team, err := q.GetTeamByID(context.Background(), teamID)
	if err != nil {
		t.Fatalf("get team: %v", err)
	}
	return team.CaptainID
}

func assertSharingSignal(t *testing.T, pool *pgxpool.Pool, submitter, owner, challenge uuid.UUID) {
	t.Helper()
	var meta string
	err := pool.QueryRow(context.Background(),
		`SELECT meta::text FROM audit_log WHERE action='flag.shared' AND subject_id=$1 ORDER BY created_at DESC LIMIT 1`,
		submitter.String()).Scan(&meta)
	if err != nil {
		t.Fatalf("no flag.shared audit row for %s: %v", submitter, err)
	}
	if !strings.Contains(meta, owner.String()) {
		t.Errorf("sharing signal meta %q does not name owner %s", meta, owner)
	}
	// The flag value must never appear in the audit meta.
	if strings.Contains(strings.ToLower(meta), "osctf{") {
		t.Errorf("SECURITY: flag value leaked into audit meta: %q", meta)
	}
}
