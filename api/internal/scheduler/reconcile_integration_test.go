//go:build integration

package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/scheduler"
)

// These drive reconcile THROUGH the fake (not only the pure function). Because the
// fake now evaluates grace against the DB clock (ReconcileClock) and rows carry a real
// Postgres updated_at, tests age a row via SQL to push it past reconcileGrace rather
// than relying on the harness's injected clock.

func ageRow(t *testing.T, h *harness, id uuid.UUID) {
	t.Helper()
	// 10 minutes > reconcileGrace (deploy timeout + 30s), so the row is eligible.
	if _, err := h.pool.Exec(context.Background(),
		`UPDATE instances SET updated_at = now() - interval '10 minutes' WHERE id = $1`, id); err != nil {
		t.Fatalf("age row: %v", err)
	}
}

func reconcileHarness(t *testing.T) *harness {
	return newHarness(t, scheduler.Config{TTL: 0, Extend: time.Minute, MaxTTL: time.Hour, Quota: 5})
}

// TestReconcileMarksVanishedContainerLost — a container disappears with its row
// unchanged; the next pass marks the row lost. Doubles as the DORMANCY GUARD: it
// asserts the pass emitted a non-zero action count, so a future change that makes
// reconcile inert (e.g. the clock-skew no-op) fails loudly here.
func TestReconcileMarksVanishedContainerLostIntegration(t *testing.T) {
	h := reconcileHarness(t)
	ctx := context.Background()
	team := h.team(t)
	ch := h.challenge(t, "per_team", "static", nil)
	inst, _, err := h.sched.Start(ctx, uuid.Nil, team, ch)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	h.fake.VanishContainer(inst.ID) // container gone, row still 'running'
	ageRow(t, h, inst.ID)

	if err := h.fake.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n := h.fake.ReconcileActionCount(); n == 0 {
		t.Fatal("reconcile over a dirty state emitted 0 actions — it went inert (dormancy)")
	}
	row, err := h.q.GetInstanceByID(ctx, inst.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.State != string(runtime.StateLost) {
		t.Errorf("row state = %q, want lost", row.State)
	}
}

// TestReconcileRemovesOrphan — a container with no backing row is removed next pass.
func TestReconcileRemovesOrphanIntegration(t *testing.T) {
	h := reconcileHarness(t)
	ctx := context.Background()
	orphan := uuid.New()
	h.fake.InjectContainer(orphan, "fake-orphan") // no DB row for this instance id

	if err := h.fake.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if containsID(h.fake.ContainerIDs(), orphan) {
		t.Error("orphan container was not removed")
	}
	// Applying reconcile again is harmless: the orphan is already gone, zero actions.
	if err := h.fake.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile again: %v", err)
	}
	if n := h.fake.ReconcileActionCount(); n != 0 {
		t.Errorf("second pass emitted %d actions, want 0 (idempotent re-apply)", n)
	}
}

// TestReconcileAdoptsCompletedDeployZeroActions — an in-sync row+container yields no
// actions and no state change, even once past grace (adoption is a no-op).
func TestReconcileAdoptsCompletedDeployZeroActionsIntegration(t *testing.T) {
	h := reconcileHarness(t)
	ctx := context.Background()
	team := h.team(t)
	ch := h.challenge(t, "per_team", "static", nil)
	inst, _, err := h.sched.Start(ctx, uuid.Nil, team, ch)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	ageRow(t, h, inst.ID) // past grace, but in sync → still nothing to do

	if err := h.fake.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n := h.fake.ReconcileActionCount(); n != 0 {
		t.Errorf("adopted deploy produced %d actions, want 0", n)
	}
	assertRunningWithContainer(t, h, inst.ID)
}

// TestReconcileIdempotentAcrossPasses — three consecutive passes over a clean in-sync
// state change nothing.
func TestReconcileIdempotentAcrossPassesIntegration(t *testing.T) {
	h := reconcileHarness(t)
	ctx := context.Background()
	team := h.team(t)
	ch := h.challenge(t, "per_team", "static", nil)
	inst, _, err := h.sched.Start(ctx, uuid.Nil, team, ch)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	ageRow(t, h, inst.ID)

	for i := 0; i < 3; i++ {
		if err := h.fake.Reconcile(ctx); err != nil {
			t.Fatalf("reconcile pass %d: %v", i, err)
		}
		if n := h.fake.ReconcileActionCount(); n != 0 {
			t.Errorf("pass %d emitted %d actions, want 0 (idempotent)", i, n)
		}
		assertRunningWithContainer(t, h, inst.ID)
	}
}

// TestReconcileRestartAdoption — after a platform restart (fresh in-memory runtime
// over the same DB, daemon containers still present), reconcile re-adopts every live
// container to its row and destroys nothing. Containers carry the v0.2 label set
// (adoption keys on instance_id, unchanged across releases), so this is also the
// v0.2-upgrade adoption case at the fake level (the no-team_id NETWORK case is the
// pure table test, since the fake has no networks).
func TestReconcileRestartAdoptionIntegration(t *testing.T) {
	h := reconcileHarness(t)
	ctx := context.Background()
	ch := h.challenge(t, "per_team", "static", nil)

	var ids []uuid.UUID
	cids := map[uuid.UUID]string{}
	for i := 0; i < 3; i++ {
		inst, _, err := h.sched.Start(ctx, uuid.Nil, h.team(t), ch)
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		row, err := h.q.GetInstanceByID(ctx, inst.ID)
		if err != nil || row.ContainerID == nil {
			t.Fatalf("row/container id for %s: %v", inst.ID, err)
		}
		ids = append(ids, inst.ID)
		cids[inst.ID] = *row.ContainerID
		ageRow(t, h, inst.ID)
	}

	// Restart: a fresh runtime (empty in-memory tracking) over the same DB. The daemon
	// still holds the containers — re-inject them so reconcile sees live containers
	// with the persisted ids matching the persisted rows.
	fresh := runtime.NewFakeRuntime(h.q)
	for _, id := range ids {
		fresh.InjectContainer(id, cids[id])
	}

	if err := fresh.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n := fresh.ReconcileActionCount(); n != 0 {
		t.Fatalf("restart reconcile emitted %d actions, want 0 (adopt all, destroy nothing)", n)
	}
	for _, id := range ids {
		row, err := h.q.GetInstanceByID(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if row.State != string(runtime.StateRunning) {
			t.Errorf("row %s state = %q, want running (must not be destroyed/lost)", id, row.State)
		}
		if !containsID(fresh.ContainerIDs(), id) {
			t.Errorf("container for %s was destroyed", id)
		}
	}
}

// TestReconcilePartialFailureContinuesAndConverges — when one action in a pass fails,
// the executor continues (does not abort), so the other actions still apply; a later
// pass then converges on the failed one.
func TestReconcilePartialFailureContinuesAndConvergesIntegration(t *testing.T) {
	h := reconcileHarness(t)
	ctx := context.Background()
	ch := h.challenge(t, "per_team", "static", nil)

	instA, _, err := h.sched.Start(ctx, uuid.Nil, h.team(t), ch)
	if err != nil {
		t.Fatalf("start A: %v", err)
	}
	instB, _, err := h.sched.Start(ctx, uuid.Nil, h.team(t), ch)
	if err != nil {
		t.Fatalf("start B: %v", err)
	}
	// Both containers vanish → two mark-lost actions this pass.
	h.fake.VanishContainer(instA.ID)
	h.fake.VanishContainer(instB.ID)
	ageRow(t, h, instA.ID)
	ageRow(t, h, instB.ID)

	// Inject a failure on A's mark-lost; B's must still apply (continue-on-failure).
	h.fake.FailReconcileActionFor = func(a runtime.Action) bool {
		return a.Kind == runtime.ActionMarkLost && a.InstanceID == instA.ID.String()
	}
	if err := h.fake.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if rowA, _ := h.q.GetInstanceByID(ctx, instA.ID); rowA.State == string(runtime.StateLost) {
		t.Error("A was marked lost despite the injected failure")
	}
	if rowB, _ := h.q.GetInstanceByID(ctx, instB.ID); rowB.State != string(runtime.StateLost) {
		t.Error("B was not marked lost — a failure on A aborted the pass instead of continuing")
	}

	// Next pass (failure cleared) converges on A.
	h.fake.FailReconcileActionFor = nil
	if err := h.fake.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile again: %v", err)
	}
	if rowA, _ := h.q.GetInstanceByID(ctx, instA.ID); rowA.State != string(runtime.StateLost) {
		t.Error("A not marked lost on the converging pass")
	}
}

func assertRunningWithContainer(t *testing.T, h *harness, id uuid.UUID) {
	t.Helper()
	row, err := h.q.GetInstanceByID(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if row.State != string(runtime.StateRunning) {
		t.Errorf("row state = %q, want running", row.State)
	}
	if !containsID(h.fake.ContainerIDs(), id) {
		t.Errorf("instance %s lost its container", id)
	}
}
