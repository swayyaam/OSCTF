//go:build integration

package scheduler_test

import (
	"context"
	"errors"
	"strings"
	"sync"
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
	fake  *runtime.FakeRuntime
	now   *time.Time // mutable; advance to drive expiry
	t0    time.Time
}

func newHarness(t *testing.T, cfg scheduler.Config) *harness {
	return newHarnessPorts(t, cfg, 30000, 30999)
}

func newHarnessPorts(t *testing.T, cfg scheduler.Config, portLo, portHi int) *harness {
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
	fake := runtime.NewFakeRuntimeWithClock(q, clk)
	mgr := runtime.NewManager(fake, q, "127.0.0.1", portLo, portHi)
	s := scheduler.New(mgr, q, events.New(q, clk), flags.NewGenerator("osctf"),
		audit.New(q, testsupport.DiscardLogger()), clk, testsupport.DiscardLogger(), cfg)
	return &harness{pool: pool, q: q, sched: s, fake: fake, now: &cur, t0: t0}
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

	// CleanupEnded is phase-gated: advance past the event end so it actually runs.
	*h.now = h.t0.Add(25 * time.Hour)
	if remaining, err := h.sched.CleanupEnded(ctx); err != nil {
		t.Fatalf("cleanup: %v", err)
	} else if remaining != 0 {
		t.Errorf("CleanupEnded left %d per-team instances, want 0", remaining)
	}
	if _, ok, _ := h.mgrInstance(ctx, chA, team); ok {
		t.Error("per-team instance survived event-end cleanup")
	}
	if _, err := h.q.GetSharedInstance(ctx, chShared); err != nil {
		t.Errorf("shared instance was destroyed by event-end cleanup: %v", err)
	}
}

// TestSchedulerCleanupEndedPhaseGatedIntegration (7-3c): the event-end teardown
// destroys every per-team instance, so it must be a no-op in any phase but ended —
// a mis-timed call must not wipe a live competition, regardless of the caller.
func TestSchedulerCleanupEndedPhaseGatedIntegration(t *testing.T) {
	h := newHarness(t, scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 5})
	ctx := context.Background()
	team := h.team(t)
	ch := h.challenge(t, "per_team", "static", nil)
	if _, _, err := h.sched.Start(ctx, uuid.Nil, team, ch); err != nil {
		t.Fatalf("start: %v", err)
	}
	alive := func(phase string) {
		t.Helper()
		if _, ok, _ := h.mgrInstance(ctx, ch, team); !ok {
			t.Fatalf("per-team instance destroyed during the %s phase — CleanupEnded is not phase-gated", phase)
		}
	}

	// Running (the default t0 window): no-op, instance survives.
	if n, err := h.sched.CleanupEnded(ctx); err != nil || n != 0 {
		t.Fatalf("running-phase CleanupEnded: n=%d err=%v, want 0-noop", n, err)
	}
	alive("running")

	// Pre (clock before start): no-op.
	*h.now = h.t0.Add(-2 * time.Hour)
	if _, err := h.sched.CleanupEnded(ctx); err != nil {
		t.Fatalf("pre-phase CleanupEnded: %v", err)
	}
	alive("pre")

	// Ended (clock past end): now it tears the instance down.
	*h.now = h.t0.Add(25 * time.Hour)
	if remaining, err := h.sched.CleanupEnded(ctx); err != nil || remaining != 0 {
		t.Fatalf("ended-phase CleanupEnded: remaining=%d err=%v, want 0", remaining, err)
	}
	if _, ok, _ := h.mgrInstance(ctx, ch, team); ok {
		t.Error("per-team instance survived event-end cleanup in the ended phase")
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

// TestSchedulerSlowDeployDoesNotBlockOthersIntegration proves the scheduler does
// not serialize every instance operation behind one slow (image-pull-length)
// deploy. While team A's deploy is blocked, team B's Start and a TTL-expiry pass
// must still make progress.
func TestSchedulerSlowDeployDoesNotBlockOthersIntegration(t *testing.T) {
	h := newHarness(t, scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 5})
	ctx := context.Background()
	teamA, teamB := h.team(t), h.team(t)
	chA, chB := h.challenge(t, "per_team", "static", nil), h.challenge(t, "per_team", "static", nil)

	blocked := make(chan struct{})
	release := make(chan struct{})
	var blockOnce, relOnce sync.Once
	doRelease := func() { relOnce.Do(func() { close(release) }) }
	h.fake.BeforeDeploy = func(_ context.Context, spec runtime.InstanceSpec) {
		if spec.TeamID != nil && *spec.TeamID == teamA {
			blockOnce.Do(func() { close(blocked) })
			<-release // hold team A's deploy until the test releases it
		}
	}

	aDone := make(chan error, 1)
	go func() { _, _, err := h.sched.Start(ctx, uuid.Nil, teamA, chA); aDone <- err }()
	<-blocked // team A is now mid-deploy (holding s.mu on the current version)

	// Team B's Start must complete while team A is blocked.
	bDone := make(chan error, 1)
	go func() { _, _, err := h.sched.Start(ctx, uuid.Nil, teamB, chB); bDone <- err }()
	select {
	case err := <-bDone:
		if err != nil {
			doRelease()
			t.Fatalf("team B Start errored: %v", err)
		}
	case <-time.After(3 * time.Second):
		doRelease()
		t.Fatal("team B's Start blocked while team A's deploy was in progress — the scheduler lock serializes all instance ops (too coarse)")
	}

	// A TTL-expiry pass must also run while team A is blocked.
	expDone := make(chan error, 1)
	go func() { expDone <- h.sched.ExpireOnce(ctx) }()
	select {
	case err := <-expDone:
		if err != nil {
			doRelease()
			t.Fatalf("ExpireOnce errored: %v", err)
		}
	case <-time.After(3 * time.Second):
		doRelease()
		t.Fatal("ExpireOnce blocked while team A's deploy was in progress")
	}

	doRelease() // let team A finish
	if err := <-aDone; err != nil {
		t.Fatalf("team A Start errored: %v", err)
	}
}

// TestSchedulerReapStaleReclaimsPortsIntegration exhausts a tiny host-port range
// with injected deploy failures (each leaves a stuck error row still holding its
// port), then proves the reaper reclaims the range — but only for rows older than
// the threshold, so a mid-deploy row is never swept.
func TestSchedulerReapStaleReclaimsPortsIntegration(t *testing.T) {
	const ports = 3
	h := newHarnessPorts(t, scheduler.Config{
		TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 10,
		ReapAfter: 15 * time.Minute,
	}, 30950, 30950+ports-1)
	ctx := context.Background()
	ch := h.challenge(t, "per_team", "static", nil)

	// Injected deploy failures: each Start reserves a port then errors, leaving a
	// stuck error row that still counts in ListUsedPorts.
	h.fake.FailDeploy = true
	for i := 0; i < ports; i++ {
		if _, _, err := h.sched.Start(ctx, uuid.Nil, h.team(t), ch); err != nil {
			t.Fatalf("seed failed-deploy %d: %v", i, err)
		}
	}

	// Range is exhausted: the next team cannot get a port.
	extra := h.team(t)
	_, _, err := h.sched.Start(ctx, uuid.Nil, extra, ch)
	if err == nil {
		t.Fatal("expected port exhaustion before reaping, but Start succeeded")
	}
	if !strings.Contains(err.Error(), "no free challenge ports") {
		t.Fatalf("expected port-exhaustion error, got: %v", err)
	}

	// Fresh rows (younger than ReapAfter) must NOT be reaped — this is what stops
	// the reaper from killing a deploy that is still in flight.
	if n, rerr := h.sched.ReapStaleOnce(ctx); rerr != nil || n != 0 {
		t.Fatalf("premature reap: n=%d err=%v, want 0 rows reaped", n, rerr)
	}

	// Age the stuck rows past the threshold (updated_at is DB wall-clock).
	if _, aerr := h.pool.Exec(ctx,
		`UPDATE instances SET updated_at = now() - interval '1 hour' WHERE state IN ('pending','error')`); aerr != nil {
		t.Fatalf("age rows: %v", aerr)
	}

	n, rerr := h.sched.ReapStaleOnce(ctx)
	if rerr != nil {
		t.Fatalf("ReapStaleOnce: %v", rerr)
	}
	if n != ports {
		t.Fatalf("reaped %d rows, want %d", n, ports)
	}

	// Ports reclaimed: the Start that could not allocate now succeeds.
	h.fake.FailDeploy = false
	inst, created, serr := h.sched.Start(ctx, uuid.Nil, extra, ch)
	if serr != nil {
		t.Fatalf("Start after reap: %v", serr)
	}
	if !created || inst.State != runtime.StateRunning {
		t.Fatalf("post-reap Start: created=%v state=%q, want created running", created, inst.State)
	}
}

// TestSchedulerReapReclaimsLostInstancePortIntegration closes the lost-row port
// leak: when a running instance's container vanishes, reconcile marks the row
// 'lost' but MarkLost keeps its host_port, ListUsedPorts still counts it, and the
// reaper used to skip 'lost' — so an abandoned lost instance leaked its port for
// the rest of the event. The reaper now reclaims 'lost' rows too.
func TestSchedulerReapReclaimsLostInstancePortIntegration(t *testing.T) {
	h := newHarnessPorts(t, scheduler.Config{
		TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 10,
		ReapAfter: 15 * time.Minute,
	}, 30960, 30960) // exactly one port
	ctx := context.Background()
	ch := h.challenge(t, "per_team", "static", nil)

	// Team A takes the only port; its container vanishes; reconcile (after the row
	// ages past the grace) marks the row lost — still holding the port.
	teamA := h.team(t)
	inst, _, err := h.sched.Start(ctx, uuid.Nil, teamA, ch)
	if err != nil {
		t.Fatalf("start A: %v", err)
	}
	h.fake.VanishContainer(inst.ID)
	ageRow(t, h, inst.ID)
	if err := h.fake.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if row, _ := h.q.GetInstanceByID(ctx, inst.ID); row.State != string(runtime.StateLost) || row.HostPort == nil {
		t.Fatalf("want a lost row still holding its port; got state=%q host_port=%v", row.State, row.HostPort)
	}

	// The leak: the lost row still holds the only port, so team B cannot start.
	teamB := h.team(t)
	if _, _, err := h.sched.Start(ctx, uuid.Nil, teamB, ch); err == nil || !strings.Contains(err.Error(), "no free challenge ports") {
		t.Fatalf("expected port exhaustion while the lost row holds the port, got: %v", err)
	}

	// A freshly-marked lost row must not be reaped yet (grace against a flicker).
	if n, rerr := h.sched.ReapStaleOnce(ctx); rerr != nil || n != 0 {
		t.Fatalf("premature reap of a fresh lost row: n=%d err=%v", n, rerr)
	}

	// Age the lost row past the threshold → the reaper reclaims it and its port.
	if _, aerr := h.pool.Exec(ctx, `UPDATE instances SET updated_at = now() - interval '1 hour' WHERE state = 'lost'`); aerr != nil {
		t.Fatalf("age lost row: %v", aerr)
	}
	if n, rerr := h.sched.ReapStaleOnce(ctx); rerr != nil || n != 1 {
		t.Fatalf("reap lost row: n=%d err=%v, want 1", n, rerr)
	}

	// Port reclaimed: team B can now start on the freed port.
	if _, _, err := h.sched.Start(ctx, uuid.Nil, teamB, ch); err != nil {
		t.Fatalf("start B after reap: %v", err)
	}
}

// TestSchedulerConcurrentStartsFillPortRangeIntegration starts N different teams
// concurrently against a port range of exactly N. Per-team locks no longer
// serialize the (global) host_port allocation, so two teams can race for the same
// port; every Start must still succeed, each on a distinct port.
func TestSchedulerConcurrentStartsFillPortRangeIntegration(t *testing.T) {
	const n = 8
	h := newHarnessPorts(t, scheduler.Config{
		TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 5,
	}, 31000, 31000+n-1)
	ctx := context.Background()
	ch := h.challenge(t, "per_team", "static", nil)

	teams := make([]uuid.UUID, n)
	for i := range teams {
		teams[i] = h.team(t)
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	ports := make([]int, n)
	start := make(chan struct{})
	for i := range teams {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release together to maximize allocation contention
			inst, _, err := h.sched.Start(ctx, uuid.Nil, teams[i], ch)
			errs[i], ports[i] = err, inst.HostPort
		}(i)
	}
	close(start)
	wg.Wait()

	seen := map[int]bool{}
	for i := range teams {
		if errs[i] != nil {
			t.Errorf("team %d Start failed: %v", i, errs[i])
			continue
		}
		if seen[ports[i]] {
			t.Errorf("team %d got duplicate host port %d", i, ports[i])
		}
		seen[ports[i]] = true
	}
	if len(seen) != n {
		t.Fatalf("got %d distinct ports, want %d (range fully and uniquely allocated)", len(seen), n)
	}
}

// TestSchedulerCleanupWaitsForInflightDeployIntegration proves CleanupEnded no
// longer deletes a row out from under an in-flight deploy (which would orphan the
// container the deploy is about to create). With the per-team lock it must wait for
// the deploy to finish, then tear the completed instance down cleanly.
func TestSchedulerCleanupWaitsForInflightDeployIntegration(t *testing.T) {
	h := newHarness(t, scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 5})
	ctx := context.Background()
	team := h.team(t)
	ch := h.challenge(t, "per_team", "static", nil)

	idCh := make(chan uuid.UUID, 1)
	release := make(chan struct{})
	h.fake.BeforeDeploy = func(_ context.Context, spec runtime.InstanceSpec) {
		if spec.TeamID != nil && *spec.TeamID == team {
			idCh <- spec.InstanceID
			<-release // hold the deploy (and the team lock) open
		}
	}

	startDone := make(chan error, 1)
	go func() { _, _, err := h.sched.Start(ctx, uuid.Nil, team, ch); startDone <- err }()
	instID := <-idCh                  // Start is now holding the team lock inside Deploy
	*h.now = h.t0.Add(25 * time.Hour) // event ended: CleanupEnded is phase-gated

	cleanupDone := make(chan error, 1)
	go func() { _, err := h.sched.CleanupEnded(ctx); cleanupDone <- err }()
	select {
	case <-cleanupDone:
		close(release)
		t.Fatal("CleanupEnded destroyed a row mid-deploy without waiting for the team lock — would orphan the container")
	case <-time.After(500 * time.Millisecond):
		// good: CleanupEnded is blocked on the team lock
	}

	close(release) // let the deploy complete
	if err := <-startDone; err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := <-cleanupDone; err != nil {
		t.Fatalf("CleanupEnded: %v", err)
	}

	// Clean teardown: the container was created (deployed) and then destroyed, and
	// the row is gone — no orphan.
	if !containsID(h.fake.DestroyedIDs(), instID) {
		t.Errorf("instance %s was never destroyed (container orphaned)", instID)
	}
	if _, gerr := h.q.GetInstanceByID(ctx, instID); gerr == nil {
		t.Errorf("instance row %s still present after CleanupEnded", instID)
	}
}

// TestSchedulerExpireDoesNotDestroyExtendedInstanceIntegration proves the BEHAVIOUR
// that matters: an Extend that commits while an expiry pass is in flight leaves the
// instance alive with the extended expiry. It asserts nothing about the locking
// implementation — the race window is created with a post-list hook, so the test
// survives a future switch to per-(team,challenge) locking.
func TestSchedulerExpireDoesNotDestroyExtendedInstanceIntegration(t *testing.T) {
	const ttl = 60
	h := newHarness(t, scheduler.Config{TTL: ttl * time.Second, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 5})
	ctx := context.Background()
	team := h.team(t)
	ch := h.challenge(t, "per_team", "static", intp(ttl))
	inst, _, err := h.sched.Start(ctx, uuid.Nil, team, ch)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if inst.ExpiresAt == nil {
		t.Fatal("started instance has no expiry to test against")
	}

	// Advance the clock so the instance is expired and will be listed.
	*h.now = h.t0.Add((ttl + 10) * time.Second)

	// Right after ExpireOnce lists the expired instance (and before it destroys any),
	// a real Extend commits — the exact interleaving the split sweep lock opened.
	var once sync.Once
	h.sched.SetExpireAfterListHookForTest(func() {
		once.Do(func() {
			if _, eerr := h.sched.Extend(ctx, team, ch); eerr != nil {
				t.Errorf("racing Extend failed: %v", eerr)
			}
		})
	})

	if e := h.sched.ExpireOnce(ctx); e != nil {
		t.Fatalf("ExpireOnce: %v", e)
	}

	// The extended instance must survive, with its expiry now in the future.
	row, gerr := h.q.GetInstanceByID(ctx, inst.ID)
	if gerr != nil {
		t.Fatalf("extended instance %s was destroyed by ExpireOnce: %v", inst.ID, gerr)
	}
	if row.ExpiresAt == nil || !row.ExpiresAt.After(*h.now) {
		t.Errorf("expiry not extended into the future: got %v, now %v", row.ExpiresAt, *h.now)
	}
	if containsID(h.fake.DestroyedIDs(), inst.ID) {
		t.Fatalf("ExpireOnce destroyed extended instance %s", inst.ID)
	}
}

// TestSchedulerCleanupEndedConvergesDespiteTimeoutIntegration proves event-end
// teardown is eventually complete even when a pass times out: with 8 teams all mid-
// deploy, a short-budget pass tears down nothing (reports 8 remaining), and once the
// deploys finish a later pass converges to 0. This is what stops instances from
// surviving past the event when several teams are deploying at the bell.
func TestSchedulerCleanupEndedConvergesDespiteTimeoutIntegration(t *testing.T) {
	const n = 8
	h := newHarness(t, scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 10})
	ctx := context.Background()
	ch := h.challenge(t, "per_team", "static", nil)

	entered := make(chan struct{}, n)
	release := make(chan struct{})
	h.fake.BeforeDeploy = func(_ context.Context, spec runtime.InstanceSpec) {
		if spec.TeamID != nil {
			entered <- struct{}{}
			<-release // hold every team's deploy (and its lock) open
		}
	}

	var startWG sync.WaitGroup
	for i := 0; i < n; i++ {
		team := h.team(t)
		startWG.Add(1)
		go func() { defer startWG.Done(); _, _, _ = h.sched.Start(ctx, uuid.Nil, team, ch) }()
	}
	for i := 0; i < n; i++ {
		<-entered // all n deploys are in flight, each holding its team lock
	}
	*h.now = h.t0.Add(25 * time.Hour) // event ended: CleanupEnded is phase-gated

	// A pass that cannot finish in time: every team is mid-deploy, so a short budget
	// tears down nothing and reports all n still present.
	shortCtx, cancelShort := context.WithTimeout(ctx, 200*time.Millisecond)
	remaining, err := h.sched.CleanupEnded(shortCtx)
	cancelShort()
	if err != nil {
		t.Fatalf("cleanup pass 1: %v", err)
	}
	if remaining != n {
		t.Fatalf("pass 1 remaining = %d, want %d (all mid-deploy, nothing torn down)", remaining, n)
	}

	// Let the deploys finish; a converged pass then tears everything down cleanly.
	close(release)
	startWG.Wait()
	remaining, err = h.sched.CleanupEnded(ctx)
	if err != nil {
		t.Fatalf("cleanup pass 2: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("pass 2 remaining = %d, want 0 (eventual convergence)", remaining)
	}
	if rows, _ := h.q.ListPerTeamInstances(ctx); len(rows) != 0 {
		t.Fatalf("%d per-team instances survived cleanup", len(rows))
	}
}

// TestSchedulerExpireSkipsBusyTeamAndRetriesIntegration proves a mid-deploy team does
// not stall expiry for other teams (its expired instance is skipped, not blocked on),
// and that the skipped expiry is picked up on a later pass rather than dropped.
func TestSchedulerExpireSkipsBusyTeamAndRetriesIntegration(t *testing.T) {
	h := newHarness(t, scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: 5})
	ctx := context.Background()
	teamA, teamB := h.team(t), h.team(t)
	chShort := h.challenge(t, "per_team", "static", intp(30))   // will expire
	chLong := h.challenge(t, "per_team", "static", intp(36000)) // won't expire in-test

	instA, _, err := h.sched.Start(ctx, uuid.Nil, teamA, chShort)
	if err != nil {
		t.Fatalf("start A short: %v", err)
	}
	instB, _, err := h.sched.Start(ctx, uuid.Nil, teamB, chShort)
	if err != nil {
		t.Fatalf("start B short: %v", err)
	}

	// teamA starts a long-TTL instance whose deploy blocks — holding teamA's lock.
	blocked := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h.fake.BeforeDeploy = func(_ context.Context, spec runtime.InstanceSpec) {
		if spec.TeamID != nil && *spec.TeamID == teamA && spec.ChallengeID == chLong {
			once.Do(func() { close(blocked) })
			<-release
		}
	}
	aDone := make(chan error, 1)
	go func() { _, _, e := h.sched.Start(ctx, uuid.Nil, teamA, chLong); aDone <- e }()
	<-blocked // teamA's lock is now held by the in-flight long deploy

	// Now chShort (30s) instances are expired; chLong (36000s) is not.
	*h.now = h.t0.Add(120 * time.Second)

	// Pass 1: teamA busy → its expired instance skipped (not blocked on); teamB free →
	// destroyed. (A blocking sweep would deadlock here on teamA's held lock.)
	if e := h.sched.ExpireOnce(ctx); e != nil {
		t.Fatalf("expire pass 1: %v", e)
	}
	if _, gerr := h.q.GetInstanceByID(ctx, instB.ID); gerr == nil {
		t.Error("teamB's expired instance not destroyed — a busy teamA must not stall other teams")
	}
	if _, gerr := h.q.GetInstanceByID(ctx, instA.ID); gerr != nil {
		t.Error("teamA's instance destroyed while teamA was mid-deploy — should have been skipped")
	}

	// Let teamA's deploy finish; a later pass picks up the skipped expiry.
	close(release)
	if e := <-aDone; e != nil {
		t.Fatalf("teamA long start: %v", e)
	}
	if e := h.sched.ExpireOnce(ctx); e != nil {
		t.Fatalf("expire pass 2: %v", e)
	}
	if _, gerr := h.q.GetInstanceByID(ctx, instA.ID); gerr == nil {
		t.Error("teamA's expired instance not picked up on a later pass")
	}
}

func containsID(ids []uuid.UUID, want uuid.UUID) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
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
