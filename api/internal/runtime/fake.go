package runtime

import (
	"context"
	"fmt"
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
	// Deployed captures every spec passed to Deploy, so tests can assert the
	// owner/network/hardening fields the Manager built.
	Deployed []InstanceSpec
}

// NewFakeRuntime builds a fake over the instances store.
func NewFakeRuntime(q *gen.Queries) *FakeRuntime {
	return &FakeRuntime{q: q, now: func() time.Time { return time.Now().UTC() }}
}

// Name implements ChallengeRuntime.
func (f *FakeRuntime) Name() string { return "fake" }

// Deploy implements ChallengeRuntime.
func (f *FakeRuntime) Deploy(ctx context.Context, spec InstanceSpec) (Instance, error) {
	if f.Unavailable {
		return Instance{}, Unavailable(fmt.Errorf("fake unavailable"))
	}
	f.Deployed = append(f.Deployed, spec)
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
func (f *FakeRuntime) Destroy(_ context.Context, _ uuid.UUID) error {
	if f.Unavailable {
		return Unavailable(fmt.Errorf("fake unavailable"))
	}
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

// Reconcile implements ChallengeRuntime.
func (f *FakeRuntime) Reconcile(_ context.Context) error { return nil }

func (f *FakeRuntime) update(ctx context.Context, id uuid.UUID, p gen.UpdateInstanceParams) (gen.Instance, error) {
	row, err := f.q.UpdateInstance(ctx, p)
	if err != nil {
		return gen.Instance{}, fmt.Errorf("fake: update instance: %w", err)
	}
	_ = id
	return row, nil
}

func ptr[T any](v T) *T { return &v }
