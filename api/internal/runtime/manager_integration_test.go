package runtime_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/testsupport"
)

// These use a real Postgres (via testsupport) but the FakeRuntime, so they need
// no Docker daemon. Named *Integration so the api-integration CI job runs them.

// uniq returns the random (non-timestamp) tail of a UUID's hex, so identifiers
// generated close together in time do not collide (UUID v7 shares its leading
// hex across a ~65s window).
func uniq(id uuid.UUID) string { return strings.ReplaceAll(id.String(), "-", "")[20:] }

func seedContainerChallenge(t *testing.T, pool *pgxpool.Pool, q *gen.Queries, instancing, flagMode string, egress bool, writablePaths string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	img := "example/img:latest"
	port := int32(8000)
	if writablePaths == "" {
		writablePaths = "[]"
	}
	if _, err := q.CreateChallenge(context.Background(), gen.CreateChallengeParams{
		ID: id, Slug: "chal-" + uniq(id), Title: "Chal", Category: "web",
		Kind: "container", Flag: "OSCTF{static_placeholder}", Scoring: "static", PointsInitial: 100,
		Image: &img, InternalPort: &port, MemLimitMb: 128, CpuMillis: 500,
		ContainerEnv: []byte("{}"), Visible: true,
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	// Set the v0.2 authoring fields directly (CreateChallenge predates them).
	if _, err := pool.Exec(context.Background(),
		`UPDATE challenges SET instancing=$2, flag_mode=$3, egress=$4, writable_paths=$5::jsonb WHERE id=$1`,
		id, instancing, flagMode, egress, writablePaths); err != nil {
		t.Fatalf("set authoring fields: %v", err)
	}
	return id
}

func seedTeam(t *testing.T, q *gen.Queries, n string) uuid.UUID {
	t.Helper()
	uid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(context.Background(), gen.CreateUserParams{
		ID: uid, Username: "u" + uniq(uid), Email: uniq(uid) + "@e.test",
		PasswordHash: "x", Role: "user",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	tid := uuid.Must(uuid.NewV7())
	if _, err := q.CreateTeam(context.Background(), gen.CreateTeamParams{
		ID: tid, Name: n + "-" + uniq(tid), InviteCode: uniq(tid), CaptainID: uid,
	}); err != nil {
		t.Fatalf("create team: %v", err)
	}
	return tid
}

func TestManagerDeployForTeamHardenedSpecIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	fake := runtime.NewFakeRuntime(q)
	mgr := runtime.NewManager(fake, q, "127.0.0.1", 30700, 30799)

	chID := seedContainerChallenge(t, pool, q, "per_team", "per_instance", false, `["/data"]`)
	teamA := seedTeam(t, q, "A")
	ctx := context.Background()

	exp := time.Now().Add(time.Hour).UTC()
	inst, err := mgr.DeployForTeam(ctx, runtime.DeployReq{
		ChallengeID: chID, TeamID: teamA, Flag: "OSCTF{team_a_unique}", ExpiresAt: &exp,
	})
	if err != nil {
		t.Fatalf("DeployForTeam: %v", err)
	}
	if inst.State != runtime.StateRunning {
		t.Fatalf("state = %q, want running", inst.State)
	}

	if len(fake.Deployed) != 1 {
		t.Fatalf("captured %d specs, want 1", len(fake.Deployed))
	}
	spec := fake.Deployed[0]
	if spec.TeamID == nil || *spec.TeamID != teamA {
		t.Errorf("spec.TeamID = %v, want %v", spec.TeamID, teamA)
	}
	if !strings.HasPrefix(spec.NetworkName, "osctf-team-") || !strings.HasSuffix(spec.NetworkName, "-int") {
		t.Errorf("spec.NetworkName = %q, want osctf-team-<id>-int (egress off)", spec.NetworkName)
	}
	if !spec.Internal {
		t.Error("spec.Internal = false, want true for egress:false")
	}
	if !spec.ReadonlyRootfs {
		t.Error("spec.ReadonlyRootfs = false, want true")
	}
	if !containsStr(spec.Tmpfs, "/tmp") || !containsStr(spec.Tmpfs, "/data") {
		t.Errorf("spec.Tmpfs = %v, want /tmp and /data", spec.Tmpfs)
	}
	if spec.Env["FLAG"] != "OSCTF{team_a_unique}" {
		t.Errorf("spec.Env[FLAG] = %q, want the per-instance flag", spec.Env["FLAG"])
	}

	// The row persisted the per-instance flag, expiry, and owner.
	row, err := q.GetTeamInstance(ctx, gen.GetTeamInstanceParams{ChallengeID: chID, TeamID: &teamA})
	if err != nil {
		t.Fatalf("GetTeamInstance: %v", err)
	}
	if row.Flag == nil || *row.Flag != "OSCTF{team_a_unique}" {
		t.Errorf("row.Flag = %v, want stored per-instance flag", row.Flag)
	}
	if row.ExpiresAt == nil {
		t.Error("row.ExpiresAt is nil, want set")
	}

	// Idempotent: a second DeployForTeam returns the same instance, no new deploy.
	if _, err := mgr.DeployForTeam(ctx, runtime.DeployReq{ChallengeID: chID, TeamID: teamA, Flag: "x"}); err != nil {
		t.Fatalf("second DeployForTeam: %v", err)
	}
	if len(fake.Deployed) != 1 {
		t.Errorf("idempotent Start re-deployed (%d specs)", len(fake.Deployed))
	}
}

func TestManagerPerTeamIsolationAndQuotaIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	fake := runtime.NewFakeRuntime(q)
	mgr := runtime.NewManager(fake, q, "127.0.0.1", 30800, 30899)
	ctx := context.Background()

	chID := seedContainerChallenge(t, pool, q, "per_team", "static", true, "")
	teamA := seedTeam(t, q, "A")
	teamB := seedTeam(t, q, "B")

	instA, err := mgr.DeployForTeam(ctx, runtime.DeployReq{ChallengeID: chID, TeamID: teamA, Flag: "OSCTF{static_placeholder}"})
	if err != nil {
		t.Fatalf("deploy A: %v", err)
	}
	instB, err := mgr.DeployForTeam(ctx, runtime.DeployReq{ChallengeID: chID, TeamID: teamB, Flag: "OSCTF{static_placeholder}"})
	if err != nil {
		t.Fatalf("deploy B: %v", err)
	}

	// Distinct ports and networks per team.
	if instA.HostPort == instB.HostPort {
		t.Errorf("teams share host port %d", instA.HostPort)
	}
	if fake.Deployed[0].NetworkName == fake.Deployed[1].NetworkName {
		t.Errorf("teams share network %q", fake.Deployed[0].NetworkName)
	}
	// egress:true static challenge -> non-internal, no stored flag.
	if fake.Deployed[0].Internal {
		t.Error("egress:true instance marked internal")
	}
	rowA, _ := q.GetTeamInstance(ctx, gen.GetTeamInstanceParams{ChallengeID: chID, TeamID: &teamA})
	if rowA.Flag != nil {
		t.Errorf("static challenge stored a per-instance flag: %v", rowA.Flag)
	}

	if n, _ := mgr.CountTeamRunning(ctx, teamA); n != 1 {
		t.Errorf("CountTeamRunning(A) = %d, want 1", n)
	}

	// Destroy A frees its row (and quota slot).
	if err := mgr.DestroyInstance(ctx, instA.ID); err != nil {
		t.Fatalf("destroy A: %v", err)
	}
	if _, ok, _ := mgr.GetTeamInstance(ctx, chID, teamA); ok {
		t.Error("team A instance row still present after destroy")
	}
	if n, _ := mgr.CountTeamRunning(ctx, teamA); n != 0 {
		t.Errorf("CountTeamRunning(A) after destroy = %d, want 0", n)
	}
	_ = instB
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
