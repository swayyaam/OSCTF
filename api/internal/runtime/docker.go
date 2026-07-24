package runtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"regexp"
	"strconv"
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
)

const (
	challengeNetwork = "osctf-challenges"
	managedLabel     = "osctf.managed"
	deployTimeout    = 120 * time.Second
	pidsLimit        = 256
	logCap           = 256 * 1024
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

	if derr := d.ensureNetwork(ctx); derr != nil {
		return Instance{}, d.wrapUnavailable(derr)
	}
	if derr := d.ensureImage(ctx, spec.Image); derr != nil {
		return Instance{}, fmt.Errorf("pulling image %s: %w", spec.Image, derr)
	}

	// Remove any stale container for this instance (retry/redeploy path).
	d.removeByLabel(ctx, spec.InstanceID)

	name := "osctf-chal-" + spec.Slug
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
			managedLabel:         "true",
			"osctf.challenge_id": spec.ChallengeID.String(),
			"osctf.instance_id":  spec.InstanceID.String(),
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
		SecurityOpt: []string{"no-new-privileges:true"},
		CapDrop:     []string{"ALL"},
	}
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{challengeNetwork: {}},
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
func (d *DockerRuntime) Reconcile(ctx context.Context) error {
	containers, err := d.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", managedLabel+"=true")),
	})
	if err != nil {
		if client.IsErrConnectionFailed(err) {
			return nil // runtime down: skip this pass, do not error the ticker
		}
		return fmt.Errorf("runtime: listing containers: %w", err)
	}
	byInstance := map[string]string{} // instance_id -> container_id
	for _, c := range containers {
		if iid := c.Labels["osctf.instance_id"]; iid != "" {
			byInstance[iid] = c.ID
		}
	}

	rows, err := d.q.ListInstances(ctx)
	if err != nil {
		return fmt.Errorf("runtime: listing instances: %w", err)
	}
	tracked := map[string]bool{}
	for _, row := range rows {
		tracked[row.ID.String()] = true
		if _, ok := byInstance[row.ID.String()]; !ok {
			// No container for this row → lost.
			if row.State != string(StateLost) {
				_, _ = d.q.UpdateInstance(ctx, gen.UpdateInstanceParams{ID: row.ID, State: strptr(string(StateLost))})
			}
			continue
		}
		if _, serr := d.Status(ctx, row.ID); serr != nil && !IsUnavailable(serr) {
			d.log.Warn("runtime: reconcile status", "instance", row.ID, "error", serr.Error())
		}
	}
	// Orphan containers (labeled, no DB row) → remove.
	for iid, cid := range byInstance {
		if !tracked[iid] {
			d.log.Warn("runtime: removing orphan container", "container", cid, "instance", iid)
			_ = d.cli.ContainerRemove(ctx, cid, container.RemoveOptions{Force: true})
		}
	}
	return nil
}

// --- helpers ---------------------------------------------------------------

func (d *DockerRuntime) ensureNetwork(ctx context.Context) error {
	nets, err := d.cli.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", challengeNetwork)),
	})
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n.Name == challengeNetwork {
			return nil
		}
	}
	_, err = d.cli.NetworkCreate(ctx, challengeNetwork, network.CreateOptions{Driver: "bridge"})
	return err
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

// health returns running/unhealthy based on a container-defined HEALTHCHECK when
// present, else a TCP dial to the published host port.
func (d *DockerRuntime) health(ctx context.Context, containerID string, hostPort int) (State, error) {
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
	if hostPort > 0 {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		var dialer net.Dialer
		conn, derr := dialer.DialContext(dctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)))
		if derr != nil {
			return StateUnhealthy, nil
		}
		_ = conn.Close()
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
