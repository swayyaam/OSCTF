package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/db/gen"
)

// FakeRuntime is an in-DB simulation used by handler/service tests that must not
// touch Docker. It moves instance rows through their states without containers.
type FakeRuntime struct {
	q   *gen.Queries
	now func() time.Time

	// FailDeploy, when set, makes Deploy mark the row errored (tests error paths).
	FailDeploy bool
	// Unavailable, when set, makes every op return a runtime-unavailable error.
	Unavailable bool
	// DeployFault, when set, makes Deploy return it before touching any state — the
	// deploy-time faults: image-pull failure, host-port-already-bound, network-create
	// failure, daemon timeout (use the Err* sentinels below).
	DeployFault error
	// BeforeDeploy, when set, is invoked at the start of Deploy (before any state
	// change). It is the injectable per-operation delay/latency mechanism (reused from
	// 1a — a test blocks or sleeps in it); it also lets a test block a specific deploy.
	BeforeDeploy func(ctx context.Context, spec InstanceSpec)
	// FailReconcileActionFor, when set and true for an action, makes Reconcile's
	// executor skip that action (simulating a mid-sequence failure) and CONTINUE with
	// the rest — the chosen partial-failure policy (best-effort, converge next pass).
	FailReconcileActionFor func(a Action) bool

	mu sync.Mutex // guards deployed/destroyed/containers (ops may run concurrently)
	// deployed captures every spec passed to Deploy; read via DeployedSpecs.
	deployed []InstanceSpec
	// destroyed captures every instance id passed to Destroy; read via DestroyedIDs.
	// Lets tests assert clean teardown (deploy-then-destroy) vs an orphaned container.
	destroyed []uuid.UUID
	// containers is the fake's simulated daemon state: instance id → its container.
	// Deploy adds, Destroy removes, and Reconcile reads it against the DB rows just
	// like DockerRuntime reads the real daemon. Fault injection perturbs it so tests
	// can drive reconcile decisions (vanished container, exited container, …).
	containers map[uuid.UUID]fakeContainer
	// lastReconcileActions is how many actions the most recent Reconcile applied; read
	// via ReconcileActionCount so a dormancy guard can assert a dirty state is not inert.
	lastReconcileActions int
}

type fakeContainer struct {
	id      string
	running bool
}

// Injectable deploy-time faults (set FakeRuntime.DeployFault to one).
var (
	ErrImagePull     = Unavailable(fmt.Errorf("fake: image pull failed"))
	ErrPortBound     = fmt.Errorf("fake: host port already bound")
	ErrNetworkCreate = Unavailable(fmt.Errorf("fake: network create failed"))
	ErrDaemonTimeout = Unavailable(fmt.Errorf("fake: daemon timeout"))
)

// VanishContainer simulates a container disappearing from the daemon WITHOUT its row
// changing (crash, manual docker rm) — the next Reconcile should mark the row lost.
func (f *FakeRuntime) VanishContainer(instanceID uuid.UUID) {
	f.mu.Lock()
	delete(f.containers, instanceID)
	f.mu.Unlock()
}

// ExitContainer simulates a container that exited immediately after start (still
// present, no longer running) — health grading, not reconcile, acts on it.
func (f *FakeRuntime) ExitContainer(instanceID uuid.UUID) {
	f.mu.Lock()
	if c, ok := f.containers[instanceID]; ok {
		c.running = false
		f.containers[instanceID] = c
	}
	f.mu.Unlock()
}

// InjectContainer adds a simulated container with no backing row — an orphan the next
// Reconcile should remove.
func (f *FakeRuntime) InjectContainer(instanceID uuid.UUID, containerID string) {
	f.mu.Lock()
	f.containers[instanceID] = fakeContainer{id: containerID, running: true}
	f.mu.Unlock()
}

// ContainerIDs returns the instance ids the fake currently has a container for (test
// visibility into the simulated daemon state).
func (f *FakeRuntime) ContainerIDs() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uuid.UUID, 0, len(f.containers))
	for id := range f.containers {
		out = append(out, id)
	}
	return out
}

// NewFakeRuntime builds a fake over the instances store.
func NewFakeRuntime(q *gen.Queries) *FakeRuntime {
	return &FakeRuntime{q: q, now: func() time.Time { return time.Now().UTC() }, containers: map[uuid.UUID]fakeContainer{}}
}

// NewFakeRuntimeWithClock builds a fake whose started_at/health timestamps come
// from the given clock, so tests injecting a clock into the scheduler see a
// consistent timeline (started_at aligns with the scheduler's now).
func NewFakeRuntimeWithClock(q *gen.Queries, now func() time.Time) *FakeRuntime {
	return &FakeRuntime{q: q, now: now, containers: map[uuid.UUID]fakeContainer{}}
}

// Name implements ChallengeRuntime.
func (f *FakeRuntime) Name() string { return "fake" }

// Deploy implements ChallengeRuntime.
func (f *FakeRuntime) Deploy(ctx context.Context, spec InstanceSpec) (Instance, error) {
	if f.Unavailable {
		return Instance{}, Unavailable(fmt.Errorf("fake unavailable"))
	}
	if f.BeforeDeploy != nil {
		f.BeforeDeploy(ctx, spec)
	}
	f.mu.Lock()
	f.deployed = append(f.deployed, spec)
	f.mu.Unlock()
	if f.DeployFault != nil {
		return Instance{}, f.DeployFault // deploy-time fault: no row change, no container
	}
	now := f.now()
	if f.FailDeploy {
		row, err := f.update(ctx, spec.InstanceID, gen.UpdateInstanceParams{
			ID: spec.InstanceID, State: ptr(string(StateError)),
			SetError: true, Error: ptr("fake deploy failure"),
		})
		if err != nil {
			return Instance{}, err
		}
		return rowToInstance(row), nil
	}
	cid := "fake-" + spec.InstanceID.String()
	row, err := f.update(ctx, spec.InstanceID, gen.UpdateInstanceParams{
		ID: spec.InstanceID, State: ptr(string(StateRunning)),
		ContainerID: &cid, StartedAt: &now, LastHealthAt: &now,
	})
	if err != nil {
		return Instance{}, err
	}
	f.mu.Lock()
	f.containers[spec.InstanceID] = fakeContainer{id: cid, running: true}
	f.mu.Unlock()
	return rowToInstance(row), nil
}

// Stop implements ChallengeRuntime.
func (f *FakeRuntime) Stop(ctx context.Context, instanceID uuid.UUID) error {
	if f.Unavailable {
		return Unavailable(fmt.Errorf("fake unavailable"))
	}
	_, err := f.update(ctx, instanceID, gen.UpdateInstanceParams{
		ID: instanceID, State: ptr(string(StateStopped)),
	})
	return err
}

// Destroy implements ChallengeRuntime (row deletion is the Manager's job).
func (f *FakeRuntime) Destroy(_ context.Context, instanceID uuid.UUID) error {
	if f.Unavailable {
		return Unavailable(fmt.Errorf("fake unavailable"))
	}
	f.mu.Lock()
	f.destroyed = append(f.destroyed, instanceID)
	delete(f.containers, instanceID)
	f.mu.Unlock()
	return nil
}

// Status implements ChallengeRuntime.
func (f *FakeRuntime) Status(ctx context.Context, instanceID uuid.UUID) (Instance, error) {
	row, err := f.q.GetInstanceByID(ctx, instanceID)
	if err != nil {
		return Instance{}, fmt.Errorf("fake: get instance: %w", err)
	}
	return rowToInstance(row), nil
}

// Logs implements ChallengeRuntime.
func (f *FakeRuntime) Logs(_ context.Context, instanceID uuid.UUID, _ int) (string, error) {
	if f.Unavailable {
		return "", Unavailable(fmt.Errorf("fake unavailable"))
	}
	return "fake logs for " + instanceID.String(), nil
}

// Reconcile implements ChallengeRuntime by running the SAME pure Reconcile decision
// as DockerRuntime, gathered from the fake's simulated container set and the DB rows,
// then executing the actions (mark lost, drop a stale/orphan container from the set).
// Networks and unadopted-flagging are Docker-only, so they are no-ops here.
func (f *FakeRuntime) Reconcile(ctx context.Context) error {
	if f.Unavailable {
		return nil // runtime down: skip the pass, like the Docker runtime
	}
	f.mu.Lock()
	observed := make([]ContainerView, 0, len(f.containers))
	for iid, c := range f.containers {
		observed = append(observed, ContainerView{ContainerID: c.id, InstanceID: iid.String(), Running: c.running})
	}
	f.mu.Unlock()

	dbRows, err := f.q.ListInstances(ctx)
	if err != nil {
		return fmt.Errorf("fake: list instances: %w", err)
	}
	rows := make([]InstanceRow, 0, len(dbRows))
	for _, r := range dbRows {
		team := ""
		if r.TeamID != nil {
			team = r.TeamID.String()
		}
		rows = append(rows, InstanceRow{
			InstanceID: r.ID.String(), TeamID: team, State: r.State,
			ContainerID: derefStrRow(r.ContainerID), UpdatedAt: r.UpdatedAt,
		})
	}

	// DB clock, not f.now(): row updated_at is written by Postgres, so grace must be
	// evaluated in the same domain (mirrors DockerRuntime; avoids the skew no-op).
	now, err := f.q.ReconcileClock(ctx)
	if err != nil {
		return fmt.Errorf("fake: db clock: %w", err)
	}
	actions, _ := Reconcile(observed, rows, nil, now)
	f.mu.Lock()
	f.lastReconcileActions = len(actions)
	f.mu.Unlock()
	for _, a := range actions {
		if f.FailReconcileActionFor != nil && f.FailReconcileActionFor(a) {
			continue // simulated executor failure: skip this action, keep going
		}
		switch a.Kind {
		case ActionMarkLost:
			id, perr := uuid.Parse(a.InstanceID)
			if perr != nil {
				continue
			}
			_, _ = f.update(ctx, id, gen.UpdateInstanceParams{ID: id, State: ptr(string(StateLost))})
		case ActionRemoveContainer:
			f.mu.Lock()
			for iid, c := range f.containers {
				if c.id == a.ContainerID {
					delete(f.containers, iid)
				}
			}
			f.mu.Unlock()
		case ActionRemoveNetwork, ActionFlagUnadopted:
			// no-op for the fake (no networks; container ids are always resolvable)
		}
	}
	return nil
}

// DeployedSpecs returns a copy of every spec passed to Deploy (concurrency-safe).
func (f *FakeRuntime) DeployedSpecs() []InstanceSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]InstanceSpec, len(f.deployed))
	copy(out, f.deployed)
	return out
}

// ReconcileActionCount returns how many actions the most recent Reconcile applied.
func (f *FakeRuntime) ReconcileActionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastReconcileActions
}

// DestroyedIDs returns a copy of every instance id passed to Destroy.
func (f *FakeRuntime) DestroyedIDs() []uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]uuid.UUID, len(f.destroyed))
	copy(out, f.destroyed)
	return out
}

func (f *FakeRuntime) update(ctx context.Context, id uuid.UUID, p gen.UpdateInstanceParams) (gen.Instance, error) {
	row, err := f.q.UpdateInstance(ctx, p)
	if err != nil {
		return gen.Instance{}, fmt.Errorf("fake: update instance: %w", err)
	}
	_ = id
	return row, nil
}

func ptr[T any](v T) *T { return &v }
