package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
)

// Manager layers DB-backed instance lifecycle over a ChallengeRuntime.
type Manager struct {
	rt         ChallengeRuntime
	q          *gen.Queries
	publicHost string
	portStart  int
	portEnd    int
}

// NewManager builds the manager.
func NewManager(rt ChallengeRuntime, q *gen.Queries, publicHost string, portStart, portEnd int) *Manager {
	return &Manager{rt: rt, q: q, publicHost: publicHost, portStart: portStart, portEnd: portEnd}
}

// Runtime exposes the underlying runtime (for the reconcile ticker).
func (m *Manager) Runtime() ChallengeRuntime { return m.rt }

// Deploy provisions (or returns the running) instance for a container challenge.
func (m *Manager) Deploy(ctx context.Context, challengeID uuid.UUID) (Instance, error) {
	ch, err := m.q.GetChallengeByID(ctx, challengeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Instance{}, apperr.ErrNotFound
		}
		return Instance{}, fmt.Errorf("runtime: get challenge: %w", err)
	}
	if ch.Kind != "container" || ch.Image == nil || ch.InternalPort == nil {
		return Instance{}, &apperr.Forbidden{Detail: "this challenge is not a container challenge"}
	}

	row, err := m.ensureRow(ctx, ch)
	if err != nil {
		return Instance{}, err
	}
	// Idempotent: an already-running instance is returned unchanged.
	if row.State == string(StateRunning) {
		return rowToInstance(row), nil
	}

	spec := InstanceSpec{
		InstanceID: row.ID, ChallengeID: ch.ID, Slug: ch.Slug,
		Image: *ch.Image, InternalPort: int(*ch.InternalPort),
		HostPort: derefPortRow(row), MemLimitMB: int(ch.MemLimitMb), CPUMillis: int(ch.CpuMillis),
		Env: decodeEnv(ch.ContainerEnv, ch.Flag),
	}
	inst, err := m.rt.Deploy(ctx, spec)
	if err != nil {
		return Instance{}, err
	}
	return inst, nil
}

// ensureRow returns the challenge's instance row, allocating a port and inserting
// a pending row when none exists.
func (m *Manager) ensureRow(ctx context.Context, ch gen.Challenge) (gen.Instance, error) {
	row, err := m.q.GetInstanceByChallenge(ctx, ch.ID)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gen.Instance{}, fmt.Errorf("runtime: get instance: %w", err)
	}
	return m.allocate(ctx, ch.ID)
}

// allocate picks the lowest free host port and inserts a pending row, retrying on
// the unique-port constraint (which arbitrates concurrent allocations).
func (m *Manager) allocate(ctx context.Context, challengeID uuid.UUID) (gen.Instance, error) {
	usedPorts, err := m.q.ListUsedPorts(ctx)
	if err != nil {
		return gen.Instance{}, fmt.Errorf("runtime: listing ports: %w", err)
	}
	used := map[int]bool{}
	for _, p := range usedPorts {
		if p != nil {
			used[int(*p)] = true
		}
	}
	for port := m.portStart; port <= m.portEnd; port++ {
		if used[port] {
			continue
		}
		id, err := uuid.NewV7()
		if err != nil {
			return gen.Instance{}, fmt.Errorf("runtime: generating id: %w", err)
		}
		p := int32(port) //nolint:gosec // G115: port is within [portStart,portEnd] <= 65535.
		row, err := m.q.CreateInstance(ctx, gen.CreateInstanceParams{
			ID: id, ChallengeID: challengeID, State: string(StatePending), HostPort: &p,
		})
		if err == nil {
			return row, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			if pgErr.ConstraintName == "uq_instances_challenge" {
				// Another request created the row first; use it.
				return m.q.GetInstanceByChallenge(ctx, challengeID)
			}
			used[port] = true // port taken concurrently; try the next one
			continue
		}
		return gen.Instance{}, fmt.Errorf("runtime: creating instance row: %w", err)
	}
	return gen.Instance{}, apperr.Conflictf("no free challenge ports in range %d-%d", m.portStart, m.portEnd)
}

// Get returns the instance for a challenge, refreshing its live status.
func (m *Manager) Get(ctx context.Context, challengeID uuid.UUID) (Instance, bool, error) {
	row, err := m.q.GetInstanceByChallenge(ctx, challengeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Instance{}, false, nil
		}
		return Instance{}, false, fmt.Errorf("runtime: get instance: %w", err)
	}
	inst, err := m.rt.Status(ctx, row.ID)
	if err != nil {
		if IsUnavailable(err) {
			return rowToInstance(row), true, nil // serve the last known state
		}
		return Instance{}, false, err
	}
	return inst, true, nil
}

// Restart stops then re-deploys the challenge's instance.
func (m *Manager) Restart(ctx context.Context, challengeID uuid.UUID) (Instance, error) {
	row, err := m.q.GetInstanceByChallenge(ctx, challengeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Instance{}, apperr.ErrNotFound
		}
		return Instance{}, fmt.Errorf("runtime: get instance: %w", err)
	}
	if err := m.rt.Stop(ctx, row.ID); err != nil {
		return Instance{}, err
	}
	return m.Deploy(ctx, challengeID)
}

// Destroy removes the container and deletes the instance row (freeing its port).
func (m *Manager) Destroy(ctx context.Context, challengeID uuid.UUID) error {
	row, err := m.q.GetInstanceByChallenge(ctx, challengeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrNotFound
		}
		return fmt.Errorf("runtime: get instance: %w", err)
	}
	if err := m.rt.Destroy(ctx, row.ID); err != nil {
		return err
	}
	if err := m.q.DeleteInstance(ctx, row.ID); err != nil {
		return fmt.Errorf("runtime: delete instance row: %w", err)
	}
	return nil
}

// DestroyForChallenge is Destroy but a no-op when no instance exists (used before
// deleting a challenge).
func (m *Manager) DestroyForChallenge(ctx context.Context, challengeID uuid.UUID) error {
	_, err := m.q.GetInstanceByChallenge(ctx, challengeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("runtime: get instance: %w", err)
	}
	return m.Destroy(ctx, challengeID)
}

// Logs returns recent container output.
func (m *Manager) Logs(ctx context.Context, challengeID uuid.UUID, tail int) (string, error) {
	row, err := m.q.GetInstanceByChallenge(ctx, challengeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apperr.ErrNotFound
		}
		return "", fmt.Errorf("runtime: get instance: %w", err)
	}
	return m.rt.Logs(ctx, row.ID, tail)
}

// Reconcile runs one reconciliation pass.
func (m *Manager) Reconcile(ctx context.Context) error { return m.rt.Reconcile(ctx) }

// ConnectionInfo renders the participant connection string, or "" unless running.
func (m *Manager) ConnectionInfo(ch gen.Challenge, inst Instance) string {
	if inst.State != StateRunning || inst.HostPort == 0 {
		return ""
	}
	tmpl := "nc {host} {port}"
	if ch.ConnectionTemplate != nil && *ch.ConnectionTemplate != "" {
		tmpl = *ch.ConnectionTemplate
	}
	r := strings.NewReplacer("{host}", m.publicHost, "{port}", strconv.Itoa(inst.HostPort))
	return r.Replace(tmpl)
}

// InstanceForChallenge returns the current instance (from the row only, no live
// inspection) for participant payloads; ok=false when there is none.
func (m *Manager) InstanceForChallenge(ctx context.Context, challengeID uuid.UUID) (Instance, bool) {
	row, err := m.q.GetInstanceByChallenge(ctx, challengeID)
	if err != nil {
		return Instance{}, false
	}
	return rowToInstance(row), true
}

func rowToInstance(row gen.Instance) Instance {
	inst := Instance{
		ID: row.ID, ChallengeID: row.ChallengeID, State: State(row.State),
		StartedAt: row.StartedAt, LastHealthAt: row.LastHealthAt,
	}
	if row.ContainerID != nil {
		inst.ContainerID = *row.ContainerID
	}
	if row.HostPort != nil {
		inst.HostPort = int(*row.HostPort)
	}
	if row.Error != nil {
		inst.Err = *row.Error
	}
	return inst
}

func derefPortRow(row gen.Instance) int {
	if row.HostPort != nil {
		return int(*row.HostPort)
	}
	return 0
}

// decodeEnv merges the challenge's container_env over the injected FLAG.
func decodeEnv(raw []byte, flag string) map[string]string {
	env := map[string]string{"FLAG": flag}
	extra := map[string]string{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &extra)
	}
	for k, v := range extra {
		env[k] = v
	}
	return env
}
