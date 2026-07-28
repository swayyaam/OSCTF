package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/db/gen"
)

// Manager layers DB-backed instance lifecycle over a ChallengeRuntime. It owns
// port allocation, instance rows, spec construction (including v0.2 hardening),
// and connection-info rendering. The shared path (team_id NULL) is v0.1
// behaviour; the per-team path is driven by the scheduler.
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

// DeployReq is a per-team deploy request from the scheduler.
type DeployReq struct {
	ChallengeID uuid.UUID
	TeamID      uuid.UUID
	Flag        string     // per-instance flag (per_instance) or the challenge flag (static)
	ExpiresAt   *time.Time // nil = no TTL
}

// --- shared path (v0.1 behaviour, team_id NULL) ------------------------------

// Deploy provisions (or returns the running) shared instance for a container challenge.
func (m *Manager) Deploy(ctx context.Context, challengeID uuid.UUID) (Instance, error) {
	ch, err := m.getContainerChallenge(ctx, challengeID)
	if err != nil {
		return Instance{}, err
	}
	row, err := m.ensureSharedRow(ctx, ch)
	if err != nil {
		return Instance{}, err
	}
	if row.State == string(StateRunning) {
		return rowToInstance(row), nil
	}
	inst, err := m.rt.Deploy(ctx, m.buildSpec(ch, row, ch.Flag))
	if err != nil {
		return Instance{}, err
	}
	return inst, nil
}

// Get returns the shared instance for a challenge, refreshing its live status.
func (m *Manager) Get(ctx context.Context, challengeID uuid.UUID) (Instance, bool, error) {
	row, err := m.q.GetSharedInstance(ctx, challengeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Instance{}, false, nil
		}
		return Instance{}, false, fmt.Errorf("runtime: get instance: %w", err)
	}
	return m.refresh(ctx, row)
}

// Restart stops then re-deploys the challenge's shared instance.
func (m *Manager) Restart(ctx context.Context, challengeID uuid.UUID) (Instance, error) {
	row, err := m.q.GetSharedInstance(ctx, challengeID)
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

// Destroy removes the shared container and deletes the row (freeing its port).
func (m *Manager) Destroy(ctx context.Context, challengeID uuid.UUID) error {
	row, err := m.q.GetSharedInstance(ctx, challengeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrNotFound
		}
		return fmt.Errorf("runtime: get instance: %w", err)
	}
	return m.DestroyInstance(ctx, row.ID)
}

// DestroyForChallenge is Destroy but a no-op when no shared instance exists.
func (m *Manager) DestroyForChallenge(ctx context.Context, challengeID uuid.UUID) error {
	_, err := m.q.GetSharedInstance(ctx, challengeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("runtime: get instance: %w", err)
	}
	return m.Destroy(ctx, challengeID)
}

// Logs returns recent output from the shared container.
func (m *Manager) Logs(ctx context.Context, challengeID uuid.UUID, tail int) (string, error) {
	row, err := m.q.GetSharedInstance(ctx, challengeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", apperr.ErrNotFound
		}
		return "", fmt.Errorf("runtime: get instance: %w", err)
	}
	return m.rt.Logs(ctx, row.ID, tail)
}

// InstanceForChallenge returns the shared instance (from the row only) for
// participant payloads; ok=false when there is none.
func (m *Manager) InstanceForChallenge(ctx context.Context, challengeID uuid.UUID) (Instance, bool) {
	row, err := m.q.GetSharedInstance(ctx, challengeID)
	if err != nil {
		return Instance{}, false
	}
	return rowToInstance(row), true
}

// --- per-team path (v0.2) ----------------------------------------------------

// DeployForTeam provisions (or returns the running) instance for a (challenge, team).
// The scheduler owns quota, flag generation, and TTL; the Manager owns rows,
// ports, networks, and hardening.
func (m *Manager) DeployForTeam(ctx context.Context, req DeployReq) (Instance, error) {
	ch, err := m.getContainerChallenge(ctx, req.ChallengeID)
	if err != nil {
		return Instance{}, err
	}
	row, err := m.ensureTeamRow(ctx, ch, req)
	if err != nil {
		return Instance{}, err
	}
	if row.State == string(StateRunning) {
		return rowToInstance(row), nil
	}
	flag := ch.Flag
	if row.Flag != nil {
		flag = *row.Flag
	}
	inst, err := m.rt.Deploy(ctx, m.buildSpec(ch, row, flag))
	if err != nil {
		return Instance{}, err
	}
	return inst, nil
}

// GetTeamInstance returns a team's instance for a challenge (from the row only).
func (m *Manager) GetTeamInstance(ctx context.Context, challengeID, teamID uuid.UUID) (Instance, bool, error) {
	row, err := m.q.GetTeamInstance(ctx, gen.GetTeamInstanceParams{ChallengeID: challengeID, TeamID: &teamID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Instance{}, false, nil
		}
		return Instance{}, false, fmt.Errorf("runtime: get team instance: %w", err)
	}
	return rowToInstance(row), true, nil
}

// ListTeamInstances returns all instances a team owns.
func (m *Manager) ListTeamInstances(ctx context.Context, teamID uuid.UUID) ([]Instance, error) {
	rows, err := m.q.ListTeamInstances(ctx, &teamID)
	if err != nil {
		return nil, fmt.Errorf("runtime: list team instances: %w", err)
	}
	out := make([]Instance, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToInstance(r))
	}
	return out, nil
}

// CountTeamRunning returns the number of running instances a team holds.
func (m *Manager) CountTeamRunning(ctx context.Context, teamID uuid.UUID) (int, error) {
	n, err := m.q.CountTeamRunningInstances(ctx, &teamID)
	if err != nil {
		return 0, fmt.Errorf("runtime: count team running: %w", err)
	}
	return int(n), nil
}

// DestroyInstance removes a container by instance id and deletes its row.
func (m *Manager) DestroyInstance(ctx context.Context, instanceID uuid.UUID) error {
	if err := m.rt.Destroy(ctx, instanceID); err != nil {
		return err
	}
	if err := m.q.DeleteInstance(ctx, instanceID); err != nil {
		return fmt.Errorf("runtime: delete instance row: %w", err)
	}
	return nil
}

// --- shared helpers ----------------------------------------------------------

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

func (m *Manager) getContainerChallenge(ctx context.Context, challengeID uuid.UUID) (gen.Challenge, error) {
	ch, err := m.q.GetChallengeByID(ctx, challengeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return gen.Challenge{}, apperr.ErrNotFound
		}
		return gen.Challenge{}, fmt.Errorf("runtime: get challenge: %w", err)
	}
	if ch.Kind != "container" || ch.Image == nil || ch.InternalPort == nil {
		return gen.Challenge{}, &apperr.Forbidden{Detail: "this challenge is not a container challenge"}
	}
	return ch, nil
}

func (m *Manager) refresh(ctx context.Context, row gen.Instance) (Instance, bool, error) {
	inst, err := m.rt.Status(ctx, row.ID)
	if err != nil {
		if IsUnavailable(err) {
			return rowToInstance(row), true, nil // serve the last known state
		}
		return Instance{}, false, err
	}
	// Status only reflects runtime fields; carry the row's owner/expiry/network.
	inst.TeamID = row.TeamID
	inst.ExpiresAt = row.ExpiresAt
	if row.Network != nil {
		inst.Network = *row.Network
	}
	return inst, true, nil
}

// buildSpec constructs the desired instance spec from the challenge and its row,
// applying v0.2 hardening defaults (read-only rootfs, tmpfs, network, egress).
func (m *Manager) buildSpec(ch gen.Challenge, row gen.Instance, flag string) InstanceSpec {
	spec := InstanceSpec{
		InstanceID:     row.ID,
		ChallengeID:    ch.ID,
		Slug:           ch.Slug,
		Image:          *ch.Image,
		InternalPort:   int(*ch.InternalPort),
		HostPort:       derefPortRow(row),
		MemLimitMB:     int(ch.MemLimitMb),
		CPUMillis:      int(ch.CpuMillis),
		Env:            decodeEnv(ch.ContainerEnv, flag),
		TeamID:         row.TeamID,
		Internal:       !ch.Egress,
		ReadonlyRootfs: true,
		Tmpfs:          append([]string{"/tmp"}, decodeWritablePaths(ch.WritablePaths)...),
	}
	if row.Network != nil {
		spec.NetworkName = *row.Network
	}
	return spec
}

func (m *Manager) ensureSharedRow(ctx context.Context, ch gen.Challenge) (gen.Instance, error) {
	row, err := m.q.GetSharedInstance(ctx, ch.ID)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gen.Instance{}, fmt.Errorf("runtime: get instance: %w", err)
	}
	net := sharedNetworkName(!ch.Egress)
	return m.allocateRow(ctx, ch.ID, nil, nil, nil, &net)
}

func (m *Manager) ensureTeamRow(ctx context.Context, ch gen.Challenge, req DeployReq) (gen.Instance, error) {
	row, err := m.q.GetTeamInstance(ctx, gen.GetTeamInstanceParams{ChallengeID: ch.ID, TeamID: &req.TeamID})
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return gen.Instance{}, fmt.Errorf("runtime: get team instance: %w", err)
	}
	net := teamNetworkName(req.TeamID, !ch.Egress)
	var flagPtr *string
	if req.Flag != "" && ch.FlagMode == "per_instance" {
		f := req.Flag
		flagPtr = &f
	}
	return m.allocateRow(ctx, ch.ID, &req.TeamID, flagPtr, req.ExpiresAt, &net)
}

// allocateRow picks the lowest free host port and inserts a pending row, retrying
// on the unique-port constraint. The owner-uniqueness constraints arbitrate
// concurrent allocations for the same (challenge[, team]).
func (m *Manager) allocateRow(ctx context.Context, challengeID uuid.UUID, teamID *uuid.UUID, flag *string, expiresAt *time.Time, network *string) (gen.Instance, error) {
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
			ID: id, ChallengeID: challengeID, TeamID: teamID, State: string(StatePending),
			HostPort: &p, Flag: flag, ExpiresAt: expiresAt, Network: network,
		})
		if err == nil {
			return row, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			switch pgErr.ConstraintName {
			case "uq_instances_shared":
				return m.q.GetSharedInstance(ctx, challengeID)
			case "uq_instances_per_team":
				return m.q.GetTeamInstance(ctx, gen.GetTeamInstanceParams{ChallengeID: challengeID, TeamID: teamID})
			default:
				used[port] = true // port taken concurrently; try the next one
				continue
			}
		}
		return gen.Instance{}, fmt.Errorf("runtime: creating instance row: %w", err)
	}
	return gen.Instance{}, apperr.Conflictf("no free challenge ports in range %d-%d", m.portStart, m.portEnd)
}

// --- pure helpers ------------------------------------------------------------

func rowToInstance(row gen.Instance) Instance {
	inst := Instance{
		ID: row.ID, ChallengeID: row.ChallengeID, TeamID: row.TeamID, State: State(row.State),
		StartedAt: row.StartedAt, LastHealthAt: row.LastHealthAt, ExpiresAt: row.ExpiresAt,
	}
	if row.ContainerID != nil {
		inst.ContainerID = *row.ContainerID
	}
	if row.HostPort != nil {
		inst.HostPort = int(*row.HostPort)
	}
	if row.Network != nil {
		inst.Network = *row.Network
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

// decodeWritablePaths parses the challenge's writable_paths jsonb array.
func decodeWritablePaths(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var paths []string
	_ = json.Unmarshal(raw, &paths)
	return paths
}

// teamNetworkName is the per-team docker bridge; the -int suffix isolates the
// internal (egress-off) variant so a team can run both egress and no-egress
// challenges without a network-mode conflict.
//
// The short id is the RANDOM tail of the team UUID, not its leading hex: UUID v7
// encodes a millisecond timestamp in its first 48 bits, so teams registering in
// the same ~65s window share their leading hex and would collide on a
// prefix-based name. The trailing 32 bits are random (birthday-safe for event
// team counts).
func teamNetworkName(teamID uuid.UUID, internal bool) string {
	s := teamID.String()
	n := "osctf-team-" + s[len(s)-8:]
	if internal {
		n += "-int"
	}
	return n
}

// sharedNetworkName is the shared bridge (v0.1) or its internal variant.
func sharedNetworkName(internal bool) string {
	if internal {
		return challengeNetwork + "-int"
	}
	return challengeNetwork
}
