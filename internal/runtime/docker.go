package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"

	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/metrics"
)

const (
	challengeNetwork = "osctf-challenges"
	teamNetPrefix    = "osctf-team-"

	// Label contract — the exact keys reconcile adopts containers/networks by.
	// Renaming any of these silently orphans resources, so a test pins them
	// (label_contract_test). Keep in sync with Deploy's applied labels.
	managedLabel     = "osctf.managed"      // "true" on every container + network we own
	instanceIDLabel  = "osctf.instance_id"  // the instance UUID a container belongs to
	challengeIDLabel = "osctf.challenge_id" // the challenge a container belongs to
	teamNetworkLabel = "osctf.team_network" // "true" on per-team bridges
	teamIDLabel      = "osctf.team_id"      // the owning team UUID on a per-team bridge

	deployTimeout = 120 * time.Second
	pidsLimit     = 256
	logCap        = 256 * 1024
	tmpfsSize     = "64m"

	// Bridge driver option that turns off outbound NAT — how an egress:false
	// challenge is cut off from the internet while staying publishable.
	masqueradeOpt = "com.docker.network.bridge.enable_ip_masquerade"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// DockerRuntime implements ChallengeRuntime against the local Docker daemon.
// It holds the instances store so it can update rows as containers change state.
type DockerRuntime struct {
	cli *client.Client
	q   *gen.Queries
	log *slog.Logger
	now func() time.Time
}

// NewDockerRuntime connects to Docker (honoring DOCKER_HOST) and negotiates the
// API version. It does not fail the platform if Docker is down — callers get a
// runtime-unavailable error at operation time.
func NewDockerRuntime(q *gen.Queries, log *slog.Logger, dockerHost string) (*DockerRuntime, error) {
	opts := []client.Opt{client.FromEnv, client.WithAPIVersionNegotiation()}
	if dockerHost != "" {
		opts = append(opts, client.WithHost(dockerHost))
	}
	cli, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("runtime: docker client: %w", err)
	}
	return &DockerRuntime{cli: cli, q: q, log: log, now: func() time.Time { return time.Now().UTC() }}, nil
}

// Name implements ChallengeRuntime.
func (d *DockerRuntime) Name() string { return "docker" }

const isoCheckPrefix = "osctf-isocheck-"

// VerifyIsolation runs a one-shot self-check of cross-network isolation: it stands
// up two throwaway per-team-style bridges (masquerade off), a listener that
// PUBLISHES a host port on one, and probes it from the other. Publishing a port is
// what defeats cross-network isolation on Docker Desktop's VM (verified: native
// Linux blocks the cross-bridge probe via DOCKER-ISOLATION, Docker Desktop does
// not), so the check mirrors a real instance by publishing.
//
// Returns (isolated, nil) on a conclusive result. A non-nil error means the check
// could not run (image/daemon/timeout) — the caller must treat isolation as
// unknown, not as a breach. The throwaway resources carry no osctf.managed label
// so Reconcile/gcTeamNetworks leave them alone; they are cleaned up here.
func (d *DockerRuntime) VerifyIsolation(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	const img = "busybox:latest"
	if err := d.ensureImage(ctx, img); err != nil {
		return false, fmt.Errorf("isolation check: image: %w", err)
	}
	id := uuid.Must(uuid.NewV7()).String()
	suf := id[len(id)-8:]
	netA, netB := isoCheckPrefix+"a-"+suf, isoCheckPrefix+"b-"+suf
	if err := d.createIsoNetwork(ctx, netA); err != nil {
		return false, fmt.Errorf("isolation check: net a: %w", err)
	}
	defer func() { _ = d.cli.NetworkRemove(context.WithoutCancel(ctx), netA) }()
	if err := d.createIsoNetwork(ctx, netB); err != nil {
		return false, fmt.Errorf("isolation check: net b: %w", err)
	}
	defer func() { _ = d.cli.NetworkRemove(context.WithoutCancel(ctx), netB) }()

	listenerID, ipB, err := d.startIsoListener(ctx, netB, img, suf)
	if listenerID != "" {
		defer func() {
			_ = d.cli.ContainerRemove(context.WithoutCancel(ctx), listenerID, container.RemoveOptions{Force: true})
		}()
	}
	if err != nil {
		return false, fmt.Errorf("isolation check: listener: %w", err)
	}

	// Positive control: the listener must be reachable from its own network (retry
	// to absorb bind latency), else the result is inconclusive rather than a breach.
	reachable := false
	for i := 0; i < 5 && !reachable; i++ {
		rc, perr := d.runIsoProbe(ctx, netB, ipB, img)
		if perr != nil {
			return false, fmt.Errorf("isolation check: positive probe: %w", perr)
		}
		reachable = rc == 0
	}
	if !reachable {
		return false, fmt.Errorf("isolation check inconclusive: listener unreachable on its own network")
	}

	// Cross-network probe: reachable from the OTHER bridge => NOT isolated.
	rc, perr := d.runIsoProbe(ctx, netA, ipB, img)
	if perr != nil {
		return false, fmt.Errorf("isolation check: cross probe: %w", perr)
	}
	return rc != 0, nil
}

func (d *DockerRuntime) createIsoNetwork(ctx context.Context, name string) error {
	_, err := d.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver:  "bridge",
		Options: map[string]string{masqueradeOpt: "false"},
	})
	return err
}

// startIsoListener runs a busybox httpd (multi-connection) on net with a published
// host port, returning its container id and its IP on net.
func (d *DockerRuntime) startIsoListener(ctx context.Context, net, img, suf string) (string, string, error) {
	const port = "9/tcp"
	created, err := d.cli.ContainerCreate(ctx,
		&container.Config{
			Image:        img,
			Cmd:          []string{"httpd", "-f", "-p", "9", "-h", "/tmp"},
			ExposedPorts: nat.PortSet{nat.Port(port): struct{}{}},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{nat.Port(port): []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: ""}}},
		},
		&network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{net: {}}},
		nil, isoCheckPrefix+"listener-"+suf)
	if err != nil {
		return "", "", err
	}
	if serr := d.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); serr != nil {
		return created.ID, "", serr
	}
	insp, ierr := d.cli.ContainerInspect(ctx, created.ID)
	if ierr != nil {
		return created.ID, "", ierr
	}
	ep, ok := insp.NetworkSettings.Networks[net]
	if !ok || ep.IPAddress == "" {
		return created.ID, "", fmt.Errorf("listener has no IP on %s", net)
	}
	return created.ID, ep.IPAddress, nil
}

// runIsoProbe runs a throwaway busybox on net that TCP-connects to ip:9, returning
// the container exit code (0 = connected/reachable).
func (d *DockerRuntime) runIsoProbe(ctx context.Context, net, ip, img string) (int, error) {
	created, err := d.cli.ContainerCreate(ctx,
		&container.Config{Image: img, Cmd: []string{"nc", "-w", "3", ip, "9"}},
		&container.HostConfig{NetworkMode: container.NetworkMode(net)},
		nil, nil, "")
	if err != nil {
		return -1, err
	}
	defer func() {
		_ = d.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
	}()
	if serr := d.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); serr != nil {
		return -1, serr
	}
	waitCh, errCh := d.cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case werr := <-errCh:
		return -1, werr
	case res := <-waitCh:
		return int(res.StatusCode), nil
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}

// Deploy implements ChallengeRuntime: pull-if-missing, create, start, health-check.
func (d *DockerRuntime) Deploy(ctx context.Context, spec InstanceSpec) (inst Instance, err error) {
	ctx, cancel := context.WithTimeout(ctx, deployTimeout)
	defer cancel()

	// On any failure or panic, mark the row errored (never leave it pending).
	defer func() {
		if err != nil && !IsUnavailable(err) {
			d.markError(context.WithoutCancel(ctx), spec.InstanceID, err.Error())
			inst, _ = d.instanceFromRow(context.WithoutCancel(ctx), spec.InstanceID)
		}
	}()

	netName := spec.NetworkName
	if netName == "" {
		netName = challengeNetwork
	}
	if derr := d.ensureNamedNetwork(ctx, netName, spec.NoEgress, spec.TeamID); derr != nil {
		return Instance{}, d.wrapUnavailable(derr)
	}
	if derr := d.ensureImage(ctx, spec.Image); derr != nil {
		return Instance{}, fmt.Errorf("pulling image %s: %w", spec.Image, derr)
	}

	// Remove any stale container for this instance (retry/redeploy path).
	d.removeByLabel(ctx, spec.InstanceID)

	// Container name must be unique among concurrent containers. A shared instance
	// keeps the v0.1 slug-only name; a per-team instance appends the instance id's
	// random tail so two teams running the same challenge do not collide.
	name := "osctf-chal-" + spec.Slug
	if spec.TeamID != nil {
		s := spec.InstanceID.String()
		name += "-" + s[len(s)-8:]
	}
	portStr := strconv.Itoa(spec.InternalPort) + "/tcp"
	exposed := nat.PortSet{nat.Port(portStr): struct{}{}}
	bindings := nat.PortMap{nat.Port(portStr): []nat.PortBinding{{
		HostIP: "0.0.0.0", HostPort: strconv.Itoa(spec.HostPort),
	}}}

	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}

	cfg := &container.Config{
		Image:        spec.Image,
		Env:          env,
		ExposedPorts: exposed,
		Labels: map[string]string{
			managedLabel:     "true",
			challengeIDLabel: spec.ChallengeID.String(),
			instanceIDLabel:  spec.InstanceID.String(),
		},
	}
	hostCfg := &container.HostConfig{
		PortBindings:  bindings,
		RestartPolicy: container.RestartPolicy{Name: "on-failure", MaximumRetryCount: 3},
		Resources: container.Resources{
			Memory:     int64(spec.MemLimitMB) * 1024 * 1024,
			MemorySwap: int64(spec.MemLimitMB) * 1024 * 1024,
			NanoCPUs:   int64(spec.CPUMillis) * 1_000_000,
			PidsLimit:  ptrInt64(pidsLimit),
		},
		SecurityOpt:    []string{"no-new-privileges:true"},
		CapDrop:        []string{"ALL"},
		ReadonlyRootfs: spec.ReadonlyRootfs,
		Tmpfs:          tmpfsMounts(spec.Tmpfs),
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{netName: {}},
	}

	created, cerr := d.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, name)
	if cerr != nil {
		return Instance{}, fmt.Errorf("creating container: %w", cerr)
	}
	if serr := d.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); serr != nil {
		return Instance{}, fmt.Errorf("starting container: %w", serr)
	}

	now := d.now()
	if _, uerr := d.q.UpdateInstance(ctx, gen.UpdateInstanceParams{
		ID: spec.InstanceID, State: strptr(string(StateStarting)),
		ContainerID: &created.ID, StartedAt: &now, SetError: true, Error: nil,
	}); uerr != nil {
		return Instance{}, fmt.Errorf("recording container: %w", uerr)
	}

	// Health: container running + TCP dial (or container HEALTHCHECK healthy).
	state, _ := d.health(ctx, created.ID, spec.HostPort)
	healthAt := d.now()
	row, uerr := d.q.UpdateInstance(ctx, gen.UpdateInstanceParams{
		ID: spec.InstanceID, State: strptr(string(state)), LastHealthAt: &healthAt,
	})
	if uerr != nil {
		return Instance{}, fmt.Errorf("recording health: %w", uerr)
	}
	return rowToInstance(row), nil
}

// Stop implements ChallengeRuntime.
func (d *DockerRuntime) Stop(ctx context.Context, instanceID uuid.UUID) error {
	row, err := d.q.GetInstanceByID(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("runtime: get instance: %w", err)
	}
	if row.ContainerID != nil {
		timeout := 10
		if serr := d.cli.ContainerStop(ctx, *row.ContainerID, container.StopOptions{Timeout: &timeout}); serr != nil {
			if client.IsErrConnectionFailed(serr) {
				return d.wrapUnavailable(serr)
			}
		}
	}
	_, err = d.q.UpdateInstance(ctx, gen.UpdateInstanceParams{ID: instanceID, State: strptr(string(StateStopped))})
	return err
}

// Destroy implements ChallengeRuntime: remove the container (row deleted by Manager).
func (d *DockerRuntime) Destroy(ctx context.Context, instanceID uuid.UUID) error {
	d.removeByLabel(ctx, instanceID)
	return nil
}

// Status implements ChallengeRuntime: inspect the live container and update the row.
func (d *DockerRuntime) Status(ctx context.Context, instanceID uuid.UUID) (Instance, error) {
	row, err := d.q.GetInstanceByID(ctx, instanceID)
	if err != nil {
		return Instance{}, fmt.Errorf("runtime: get instance: %w", err)
	}
	if row.ContainerID == nil {
		return rowToInstance(row), nil
	}
	insp, ierr := d.cli.ContainerInspect(ctx, *row.ContainerID)
	if ierr != nil {
		if cerrdefs.IsNotFound(ierr) {
			row, _ = d.q.UpdateInstance(ctx, gen.UpdateInstanceParams{ID: instanceID, State: strptr(string(StateLost))})
			return rowToInstance(row), nil
		}
		if client.IsErrConnectionFailed(ierr) {
			return rowToInstance(row), d.wrapUnavailable(ierr)
		}
		return Instance{}, fmt.Errorf("inspecting container: %w", ierr)
	}
	state := StateStopped
	if insp.State.Running {
		state, _ = d.health(ctx, *row.ContainerID, derefPortRow(row))
	}
	healthAt := d.now()
	row, err = d.q.UpdateInstance(ctx, gen.UpdateInstanceParams{
		ID: instanceID, State: strptr(string(state)), LastHealthAt: &healthAt,
	})
	if err != nil {
		return Instance{}, err
	}
	return rowToInstance(row), nil
}

// Logs implements ChallengeRuntime.
func (d *DockerRuntime) Logs(ctx context.Context, instanceID uuid.UUID, tail int) (string, error) {
	row, err := d.q.GetInstanceByID(ctx, instanceID)
	if err != nil {
		return "", fmt.Errorf("runtime: get instance: %w", err)
	}
	if row.ContainerID == nil {
		return "", nil
	}
	rc, lerr := d.cli.ContainerLogs(ctx, *row.ContainerID, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(tail),
	})
	if lerr != nil {
		if client.IsErrConnectionFailed(lerr) {
			return "", d.wrapUnavailable(lerr)
		}
		return "", fmt.Errorf("reading logs: %w", lerr)
	}
	defer func() { _ = rc.Close() }()

	var out limitedWriter
	out.cap = logCap
	if _, cerr := stdcopy.StdCopy(&out, &out, rc); cerr != nil && !errors.Is(cerr, io.EOF) {
		// Partial logs are still useful; return what we have.
		d.log.Warn("runtime: log demux", "error", cerr.Error())
	}
	return ansiRe.ReplaceAllString(out.String(), ""), nil
}

// Reconcile implements ChallengeRuntime: align DB rows with live containers.
// Reconcile aligns the daemon with the DB in three steps: gather what the daemon
// and DB show, call the pure Reconcile decision, execute the returned Actions.
// Health grading of still-present instances is a separate live-inspect side effect
// (refreshHealth), kept out of the pure decision.
func (d *DockerRuntime) Reconcile(ctx context.Context) error {
	observed, rowViews, networks, dbNow, ok, err := d.gatherReconcile(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil // runtime down: skip this pass, do not error the ticker
	}

	// dbNow (not the app clock) so a skewed app host cannot make every row read fresh.
	actions, stats := Reconcile(observed, rowViews, networks, dbNow)
	var unadoptedC, unadoptedN int
	touched := map[string]bool{} // instance ids an action changed this pass
	// Partial-failure policy: CONTINUE on a failed action (executeAction logs and
	// swallows), never abort the pass — one wedged container/network must not block
	// the rest, and reconcile is idempotent so the next pass retries the failure.
	for _, a := range actions {
		d.executeAction(ctx, a)
		metrics.ReconcileActionsTotal.WithLabelValues(a.Kind.metricLabel()).Inc()
		switch a.Kind {
		case ActionFlagUnadopted:
			unadoptedC++
		case ActionFlagUnadoptedNetwork:
			unadoptedN++
		case ActionMarkLost:
			touched[a.InstanceID] = true
		}
	}
	metrics.UnadoptedContainers.Set(float64(unadoptedC))
	metrics.UnadoptedNetworks.Set(float64(unadoptedN))
	metrics.ReconcileActions.Set(float64(len(actions)))
	metrics.ReconcileGraceSkipped.Set(float64(stats.SkippedGrace))
	if stats.FutureRows > 0 {
		metrics.ReconcileFutureRows.Add(float64(stats.FutureRows))
		d.log.Warn("runtime: reconcile saw rows with updated_at ahead of the DB clock; skipped conservatively",
			"future_rows", stats.FutureRows)
	}

	d.refreshHealth(ctx, observed, rowViews, touched)
	metrics.MarkSuccess(metrics.ReconcileLastSuccess) // liveness heartbeat
	return nil
}

// ListUnadopted returns managed containers whose osctf.instance_id label is missing
// or unparseable — the ones reconcile flags but never removes. Surfaced in the admin
// fleet view. A daemon-down error is swallowed (empty list, not an error).
func (d *DockerRuntime) ListUnadopted(ctx context.Context) ([]UnadoptedContainer, error) {
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", managedLabel+"=true")),
	})
	if err != nil {
		if client.IsErrConnectionFailed(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("runtime: listing containers: %w", err)
	}
	var out []UnadoptedContainer
	for _, c := range containers {
		if id := c.Labels[instanceIDLabel]; id != "" {
			if _, perr := uuid.Parse(id); perr == nil {
				continue // resolvable → not unadopted
			}
		}
		out = append(out, UnadoptedContainer{
			ContainerID: c.ID,
			Image:       c.Image,
			CreatedAt:   time.Unix(c.Created, 0).UTC(),
		})
	}
	return out, nil
}

// ListUnadoptedNetworks returns managed per-team bridges with no resolvable team_id
// label — the ones reconcile flags but never GC's. Surfaced in the admin fleet view.
func (d *DockerRuntime) ListUnadoptedNetworks(ctx context.Context) ([]UnadoptedNetwork, error) {
	nets, err := d.cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", managedLabel+"=true")),
	})
	if err != nil {
		if client.IsErrConnectionFailed(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("runtime: listing networks: %w", err)
	}
	var out []UnadoptedNetwork
	for _, n := range nets {
		isTeam := strings.HasPrefix(n.Name, teamNetPrefix) || n.Labels[teamNetworkLabel] == "true"
		if !isTeam {
			continue
		}
		if id := n.Labels[teamIDLabel]; id != "" {
			if _, perr := uuid.Parse(id); perr == nil {
				continue // resolvable → not unadopted
			}
		}
		out = append(out, UnadoptedNetwork{NetworkID: n.ID, Name: n.Name})
	}
	return out, nil
}

// gatherReconcile collects the reconcile inputs, including the DB clock (now) that
// grace is evaluated against. ok=false means the daemon is unreachable and the pass
// should be skipped without erroring.
func (d *DockerRuntime) gatherReconcile(ctx context.Context) (observed []ContainerView, rows []InstanceRow, networks []NetworkView, now time.Time, ok bool, err error) {
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", managedLabel+"=true")),
	})
	if err != nil {
		if client.IsErrConnectionFailed(err) {
			return nil, nil, nil, time.Time{}, false, nil
		}
		return nil, nil, nil, time.Time{}, false, fmt.Errorf("runtime: listing containers: %w", err)
	}
	observed = make([]ContainerView, 0, len(containers))
	for _, c := range containers {
		observed = append(observed, ContainerView{
			ContainerID: c.ID,
			InstanceID:  c.Labels[instanceIDLabel],
			Running:     c.State == "running",
		})
	}

	dbRows, err := d.q.ListInstances(ctx)
	if err != nil {
		return nil, nil, nil, time.Time{}, false, fmt.Errorf("runtime: listing instances: %w", err)
	}
	rows = make([]InstanceRow, 0, len(dbRows))
	for _, r := range dbRows {
		team := ""
		if r.TeamID != nil {
			team = r.TeamID.String()
		}
		rows = append(rows, InstanceRow{
			InstanceID:  r.ID.String(),
			TeamID:      team,
			State:       r.State,
			ContainerID: derefStrRow(r.ContainerID),
			UpdatedAt:   r.UpdatedAt,
		})
	}

	networks = d.gatherTeamNetworks(ctx)

	now, err = d.q.ReconcileClock(ctx)
	if err != nil {
		return nil, nil, nil, time.Time{}, false, fmt.Errorf("runtime: reading db clock: %w", err)
	}
	return observed, rows, networks, now, true, nil
}

// gatherTeamNetworks projects managed per-team bridges (with attached counts) into
// NetworkViews. Connection failures are tolerated (empty slice → no GC this pass).
func (d *DockerRuntime) gatherTeamNetworks(ctx context.Context) []NetworkView {
	nets, err := d.cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", managedLabel+"=true")),
	})
	if err != nil {
		return nil
	}
	var out []NetworkView
	for _, n := range nets {
		if !strings.HasPrefix(n.Name, teamNetPrefix) && n.Labels[teamNetworkLabel] != "true" {
			continue
		}
		full, ierr := d.cli.NetworkInspect(ctx, n.ID, network.InspectOptions{})
		if ierr != nil {
			continue // can't inspect → don't propose a GC we can't justify
		}
		out = append(out, NetworkView{
			NetworkID: n.ID, Name: n.Name, TeamID: n.Labels[teamIDLabel],
			TeamNetwork: true, Attached: len(full.Containers),
		})
	}
	return out
}

// executeAction carries out one reconcile Action against Docker/the DB.
func (d *DockerRuntime) executeAction(ctx context.Context, a Action) {
	switch a.Kind {
	case ActionMarkLost:
		id, perr := uuid.Parse(a.InstanceID)
		if perr != nil {
			return
		}
		if _, uerr := d.q.UpdateInstance(ctx, gen.UpdateInstanceParams{ID: id, State: strptr(string(StateLost))}); uerr != nil {
			d.log.Warn("runtime: reconcile mark-lost", "instance", a.InstanceID, "error", uerr.Error())
		}
	case ActionRemoveContainer:
		d.log.Warn("runtime: reconcile removing container", "container", a.ContainerID, "reason", a.Reason)
		_ = d.cli.ContainerRemove(ctx, a.ContainerID, container.RemoveOptions{Force: true})
	case ActionRemoveNetwork:
		if rerr := d.cli.NetworkRemove(ctx, a.NetworkID); rerr != nil {
			d.log.Warn("runtime: reconcile removing network", "network", a.NetworkID, "error", rerr.Error())
		}
	case ActionFlagUnadopted:
		// Never removed (we cannot identify it), but surfaced: a managed container
		// holding a port with no resolvable instance is an operator problem.
		d.log.Warn("runtime: unadopted managed container (unresolvable instance_id)",
			"container", a.ContainerID)
	case ActionFlagUnadoptedNetwork:
		// Never GC'd (no team_id → cannot tell in-flight from abandoned); surfaced so
		// an organizer can remove a bridge left by a pre-team_id-label release.
		d.log.Warn("runtime: unadopted team network (no resolvable team_id)",
			"network", a.NetworkID)
	}
}

// refreshHealth re-inspects the still-present instances to update their health/state
// (running→unhealthy/stopped/lost). It is a live-inspect side effect, deliberately
// separate from the pure Reconcile decision. It skips any instance an action already
// changed this pass (touched) so it cannot contradict a decision — e.g. resurrect a
// row just marked lost (2a-iv). In practice marked-lost rows have no container and so
// are absent from `present` anyway; the skip makes that guarantee explicit.
func (d *DockerRuntime) refreshHealth(ctx context.Context, observed []ContainerView, rows []InstanceRow, touched map[string]bool) {
	present := make(map[string]bool, len(observed))
	for _, c := range observed {
		if c.InstanceID != "" {
			present[c.InstanceID] = true
		}
	}
	for _, r := range rows {
		if !present[r.InstanceID] || touched[r.InstanceID] {
			continue
		}
		id, perr := uuid.Parse(r.InstanceID)
		if perr != nil {
			continue
		}
		if _, serr := d.Status(ctx, id); serr != nil && !IsUnavailable(serr) {
			d.log.Warn("runtime: reconcile status", "instance", r.InstanceID, "error", serr.Error())
		}
	}
}

// --- helpers ---------------------------------------------------------------

// ensureNamedNetwork creates the challenge bridge if it is missing. When
// noEgress is set the bridge is built WITHOUT outbound NAT rather than with
// Docker's `internal` flag.
//
// Why not `Internal: true`: an internal bridge has no gateway, so Docker
// silently discards the container's published-port bindings — the host port is
// allocated and advertised to players but nothing ever listens on it. That made
// every egress:false challenge (including both per-team examples) unreachable.
// Disabling ip_masquerade instead leaves inbound DNAT intact while the
// container's outbound packets leave unmasqueraded, so replies cannot route
// back: no internet from the challenge, but players can still connect.
//
// Two things this is weaker at than an internal bridge, both measured:
//   - the bridge gateway stays addressable, so a challenge can reach host
//     services and any PUBLISHED port (including the platform's own API).
//     Team-to-team isolation is unaffected: unpublished ports on another
//     bridge stay unreachable.
//   - it relies on the host not re-masquerading. Docker Desktop NATs again at
//     the VM boundary, so egress is NOT blocked there; on a Linux host (the
//     documented deployment target) the missing MASQUERADE rule does block it.
//
// Closing both needs instance traffic proxied through the platform instead of
// published on host ports — see docs/v0.2/03-runtime.md.
func (d *DockerRuntime) ensureNamedNetwork(ctx context.Context, name string, noEgress bool, teamID *uuid.UUID) error {
	nets, err := d.cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n.Name != name {
			continue // the name filter is a substring match
		}
		// Rebuild a bridge left over from the `internal` scheme, otherwise the
		// port-publishing bug survives the upgrade. Only when it is idle; a
		// network still carrying containers is left alone until they are gone.
		if !noEgress || !d.isIdleInternalNetwork(ctx, n.ID) {
			return nil
		}
		if rerr := d.cli.NetworkRemove(ctx, n.ID); rerr != nil {
			d.log.Warn("runtime: replacing legacy internal network",
				"network", name, "error", rerr.Error())
			return nil
		}
		break
	}
	labels := map[string]string{managedLabel: "true"}
	if strings.HasPrefix(name, teamNetPrefix) {
		labels[teamNetworkLabel] = "true"
		if teamID != nil {
			// Full team UUID so reconcile can match this bridge to that team's rows
			// and hold off GC while a deploy for the team is in flight (2a-ii).
			labels[teamIDLabel] = teamID.String()
		}
	}
	var opts map[string]string
	if noEgress {
		opts = map[string]string{masqueradeOpt: "false"}
	}
	_, err = d.cli.NetworkCreate(ctx, name, network.CreateOptions{
		Driver:  "bridge",
		Options: opts,
		Labels:  labels,
	})
	return err
}

// isIdleInternalNetwork reports whether id is an `internal` bridge with nothing
// attached — i.e. safe to remove and recreate under the current scheme.
func (d *DockerRuntime) isIdleInternalNetwork(ctx context.Context, id string) bool {
	full, err := d.cli.NetworkInspect(ctx, id, network.InspectOptions{})
	if err != nil {
		return false
	}
	return full.Internal && len(full.Containers) == 0
}

// tmpfsMounts renders tmpfs mount targets into Docker's Tmpfs map. Each is small,
// noexec, nosuid so a read-only-rootfs challenge still has scratch space.
func tmpfsMounts(targets []string) map[string]string {
	if len(targets) == 0 {
		return nil
	}
	out := make(map[string]string, len(targets))
	for _, t := range targets {
		out[t] = "rw,noexec,nosuid,size=" + tmpfsSize
	}
	return out
}

func (d *DockerRuntime) ensureImage(ctx context.Context, ref string) error {
	// Pull policy: if-not-present (local example builds must win).
	if _, err := d.cli.ImageInspect(ctx, ref); err == nil {
		return nil
	}
	rc, err := d.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		if client.IsErrConnectionFailed(err) {
			return d.wrapUnavailable(err)
		}
		return err
	}
	defer func() { _ = rc.Close() }()
	_, _ = io.Copy(io.Discard, rc) // block until the pull completes
	return nil
}

// health returns the instance state from the live container. A container-defined
// HEALTHCHECK is authoritative when present; otherwise a running container is
// healthy.
//
// Note (decision log, docs/v0.1/08): the spec's extra "TCP dial to
// 127.0.0.1:<HostPort>" assumed a host-networked platform. In the golden-path
// compose deployment the platform runs in its own container and cannot reach the
// host-published port on 127.0.0.1, so the dial is dropped — the container's
// running state (plus its own HEALTHCHECK) is the health signal.
func (d *DockerRuntime) health(ctx context.Context, containerID string, _ int) (State, error) {
	insp, err := d.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return StateError, err
	}
	if !insp.State.Running {
		return StateStopped, nil
	}
	if insp.State.Health != nil {
		if insp.State.Health.Status == "healthy" {
			return StateRunning, nil
		}
		return StateUnhealthy, nil
	}
	return StateRunning, nil
}

func (d *DockerRuntime) removeByLabel(ctx context.Context, instanceID uuid.UUID) {
	list, err := d.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "osctf.instance_id="+instanceID.String())),
	})
	if err != nil {
		return
	}
	for _, c := range list {
		_ = d.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
	}
}

func (d *DockerRuntime) markError(ctx context.Context, instanceID uuid.UUID, msg string) {
	_, _ = d.q.UpdateInstance(ctx, gen.UpdateInstanceParams{
		ID: instanceID, State: strptr(string(StateError)), SetError: true, Error: &msg,
	})
}

func (d *DockerRuntime) instanceFromRow(ctx context.Context, instanceID uuid.UUID) (Instance, error) {
	row, err := d.q.GetInstanceByID(ctx, instanceID)
	if err != nil {
		return Instance{}, err
	}
	return rowToInstance(row), nil
}

func (d *DockerRuntime) wrapUnavailable(err error) error { return Unavailable(err) }

func ptrInt64(v int64) *int64 { return &v }
func strptr(s string) *string { return &s }

func derefStrRow(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// limitedWriter accumulates up to cap bytes, discarding the rest.
type limitedWriter struct {
	buf []byte
	cap int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if remaining := w.cap - len(w.buf); remaining > 0 {
		if len(p) > remaining {
			w.buf = append(w.buf, p[:remaining]...)
		} else {
			w.buf = append(w.buf, p...)
		}
	}
	return len(p), nil
}

func (w *limitedWriter) String() string { return string(w.buf) }
