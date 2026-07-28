package scheduler_test

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
	"github.com/osctf/platform/internal/flags"
	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/scheduler"
	"github.com/osctf/platform/internal/testsupport"
)

func uniq(id uuid.UUID) string { return strings.ReplaceAll(id.String(), "-", "")[20:] }

// harness wires a scheduler over a FakeRuntime with a mutable clock.
type harness struct {
	pool  *pgxpool.Pool
	q     *gen.Queries
	sched *scheduler.Scheduler
	now   *time.Time // mutable; advance to drive expiry
	t0    time.Time
}

func newHarness(t *testing.T, cfg scheduler.Config) *harness {
	t.Helper()
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cur := t0
	clk := clock.Clock(func() time.Time { return cur })
	// Running event window around t0.
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: t0.Add(-time.Hour), EndsAt: t0.Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	mgr := runtime.NewManager(runtime.NewFakeRuntimeWithClock(q, clk), q, "127.0.0.1", 30000, 30999)
	s := scheduler.New(mgr, q, events.New(q, clk), flags.NewGenerator("osctf"),
		audit.New(q, testsupport.DiscardLogger()), clk, testsupport.DiscardLogger(), cfg)
	return &harness{pool: pool, q: q, sched: s, now: &cur, t0: t0}
}

func (h *harness) challenge(t *testing.T, instancing, flagMode string, ttlSeconds *int) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	img := "x/y:latest"
	port := int32(8000)
	if _, err := h.q.CreateChallenge(context.Background(), gen.CreateChallengeParams{
		ID: id, Slug: "c-" + uniq(id), Title: "C", Category: "web", Kind: "container",
		Flag: "OSCTF{static}", Scoring: "static", PointsInitial: 100,
		Image: &img, InternalPort: &port, MemLimitMb: 128, CpuMillis: 500,
		ContainerEnv: []byte("{}"), Visible: true,
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	if _, err := h.pool.Exec(context.Background(),
		`UPDATE challenges SET instancing=$2, flag_mode=$3, instance_ttl_seconds=$4 WHERE id=$1`,
		id, instancing, flagMode, ttlSeconds); err != nil {
		t.Fatalf("set fields: %v", err)
	}
	return id
}

func (h *harness) team(t *testing.T) uuid.UUID {
	t.Helper()
	uid := uuid.Must(uuid.NewV7())
	if _, err := h.q.CreateUser(context.Background(), gen.CreateUserParams{
		ID: uid, Username: "u" + uniq(uid), Email: uniq(uid) + "@e.test", PasswordHash: "x", Role: "user",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	tid := uuid.Must(uuid.NewV7())
	if _, err := h.q.CreateTeam(context.Background(), gen.CreateTeamParams{
		ID: tid, Name: "t" + uniq(tid), InviteCode: uniq(tid), CaptainID: uid,
	}); err != nil {
		t.Fatalf("create team: %v", err)
	}
	return tid
}

func intp(n int) *int { return &n }

func TestSchedulerStartIdempotentAndQuotaIntegration(t *testing.T) {
	h := newHarness(t, scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 2})
	ctx := context.Background()
	team := h.team(t)
	chA, chB, chC := h.challenge(t, "per_team", "static", nil), h.challenge(t, "per_team", "static", nil), h.challenge(t, "per_team", "static", nil)

	inst1, created, err := h.sched.Start(ctx, uuid.Nil, team, chA)
	if err != nil || !created {
		t.Fatalf("start A: inst=%v created=%v err=%v", inst1.ID, created, err)
	}
	// Idempotent: same instance, not created again.
	inst2, created, err := h.sched.Start(ctx, uuid.Nil, team, chA)
	if err != nil {
		t.Fatalf("start A again: %v", err)
	}
	if created || inst2.ID != inst1.ID {
		t.Errorf("second Start not idempotent: created=%v id=%v vs %v", created, inst2.ID, inst1.ID)
	}

	if _, _, err := h.sched.Start(ctx, uuid.Nil, team, chB); err != nil {
		t.Fatalf("start B: %v", err)
	}
	// Quota is 2; the third challenge must be rejected.
	_, _, err = h.sched.Start(ctx, uuid.Nil, team, chC)
	if !isConflict(err) {
		t.Fatalf("start C at quota: err=%v, want conflict", err)
	}
	// Stopping B frees a slot.
	if err := h.sched.Stop(ctx, uuid.Nil, team, chB); err != nil {
		t.Fatalf("stop B: %v", err)
	}
	if _, created, err := h.sched.Start(ctx, uuid.Nil, team, chC); err != nil || !created {
		t.Fatalf("start C after freeing slot: created=%v err=%v", created, err)
	}
}

func TestSchedulerExtendCapIntegration(t *testing.T) {
	h := newHarness(t, scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 2 * time.Hour, Quota: 5})
	ctx := context.Background()
	team := h.team(t)
	ch := h.challenge(t, "per_team", "static", nil)

	inst, _, err := h.sched.Start(ctx, uuid.Nil, team, ch)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	firstExp := *inst.ExpiresAt // t0 + 1h

	// Extend adds the step to the current expiry (does not shorten a fresh instance).
	ext, err := h.sched.Extend(ctx, team, ch)
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if !ext.ExpiresAt.After(firstExp) {
		t.Errorf("extend did not push expiry forward: %v -> %v", firstExp, *ext.ExpiresAt)
	}
	// started_at ~ t0, MaxTTL 2h => cap at t0+2h. Started at 1h; +30m each => 1h30, 2h(cap).
	// One more extend reaches the cap; the next must be rejected.
	for i := 0; i < 5; i++ {
		_, err = h.sched.Extend(ctx, team, ch)
		if isConflict(err) {
			return // hit the max-lifetime cap as expected
		}
		if err != nil {
			t.Fatalf("extend %d: %v", i, err)
		}
	}
	t.Fatal("extend never hit the max-lifetime cap")
}

func TestSchedulerExpiryIntegration(t *testing.T) {
	h := newHarness(t, scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 5})
	ctx := context.Background()
	team := h.team(t)
	ch := h.challenge(t, "per_team", "static", nil)

	inst, _, err := h.sched.Start(ctx, uuid.Nil, team, ch)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if inst.ExpiresAt == nil {
		t.Fatal("instance has no expiry")
	}

	// Not yet expired: a pass at t0 does nothing.
	if err := h.sched.ExpireOnce(ctx); err != nil {
		t.Fatalf("expire (early): %v", err)
	}
	if _, ok, _ := h.mgrInstance(ctx, ch, team); !ok {
		t.Fatal("instance destroyed before its TTL")
	}

	// Advance past the TTL and run one pass: the instance is destroyed.
	*h.now = h.t0.Add(2 * time.Hour)
	if err := h.sched.ExpireOnce(ctx); err != nil {
		t.Fatalf("expire: %v", err)
	}
	if _, ok, _ := h.mgrInstance(ctx, ch, team); ok {
		t.Error("instance survived past its TTL")
	}
}

func TestSchedulerCleanupEndedIntegration(t *testing.T) {
	h := newHarness(t, scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 5})
	ctx := context.Background()
	team := h.team(t)
	chA := h.challenge(t, "per_team", "static", nil)
	chShared := h.challenge(t, "shared", "static", nil)

	if _, _, err := h.sched.Start(ctx, uuid.Nil, team, chA); err != nil {
		t.Fatalf("start per-team: %v", err)
	}
	// A shared instance (admin-style deploy) must survive event-end cleanup.
	mgr := runtime.NewManager(runtime.NewFakeRuntime(h.q), h.q, "127.0.0.1", 31000, 31099)
	if _, err := mgr.Deploy(ctx, chShared); err != nil {
		t.Fatalf("deploy shared: %v", err)
	}

	if err := h.sched.CleanupEnded(ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, ok, _ := h.mgrInstance(ctx, chA, team); ok {
		t.Error("per-team instance survived event-end cleanup")
	}
	if _, err := h.q.GetSharedInstance(ctx, chShared); err != nil {
		t.Errorf("shared instance was destroyed by event-end cleanup: %v", err)
	}
}

func TestSchedulerTTLOverrideIntegration(t *testing.T) {
	h := newHarness(t, scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 5})
	ctx := context.Background()

	// instance_ttl_seconds = 0 => no TTL.
	teamNo := h.team(t)
	chNoTTL := h.challenge(t, "per_team", "static", intp(0))
	instNo, _, err := h.sched.Start(ctx, uuid.Nil, teamNo, chNoTTL)
	if err != nil {
		t.Fatalf("start no-ttl: %v", err)
	}
	if instNo.ExpiresAt != nil {
		t.Errorf("ttl=0 challenge got an expiry: %v", instNo.ExpiresAt)
	}

	// instance_ttl_seconds = 300 => expiry ~ now + 5m (overrides the 1h default).
	teamOv := h.team(t)
	chOverride := h.challenge(t, "per_team", "static", intp(300))
	instOv, _, err := h.sched.Start(ctx, uuid.Nil, teamOv, chOverride)
	if err != nil {
		t.Fatalf("start override: %v", err)
	}
	if instOv.ExpiresAt == nil {
		t.Fatal("override challenge has no expiry")
	}
	want := h.t0.Add(5 * time.Minute)
	if instOv.ExpiresAt.Sub(want).Abs() > time.Second {
		t.Errorf("override expiry = %v, want ~%v", *instOv.ExpiresAt, want)
	}
}

func (h *harness) mgrInstance(ctx context.Context, challengeID, teamID uuid.UUID) (gen.Instance, bool, error) {
	row, err := h.q.GetTeamInstance(ctx, gen.GetTeamInstanceParams{ChallengeID: challengeID, TeamID: &teamID})
	if err != nil {
		return gen.Instance{}, false, nil //nolint:nilerr // absence is not an error here
	}
	return row, true, nil
}

func isConflict(err error) bool {
	var c *apperr.Conflict
	return errors.As(err, &c)
}
