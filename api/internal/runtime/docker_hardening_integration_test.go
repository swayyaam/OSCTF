//go:build dockerint

// Real-daemon tests for the v0.2 hardening pass and per-team network isolation.
// Run with: go test -tags dockerint ./internal/runtime/...
package runtime_test

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/uuid"

	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/testsupport"
)

func dockerClient(t *testing.T) *client.Client {
	t.Helper()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func containerForInstance(t *testing.T, cli *client.Client, instanceID uuid.UUID) container.InspectResponse {
	t.Helper()
	list, err := cli.ContainerList(context.Background(), container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "osctf.instance_id="+instanceID.String())),
	})
	if err != nil || len(list) == 0 {
		t.Fatalf("find container for %s: err=%v n=%d", instanceID, err, len(list))
	}
	insp, err := cli.ContainerInspect(context.Background(), list[0].ID)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	return insp
}

// TestDockerHardeningIntegration asserts the hardening pass is applied to a
// deployed container and that egress:false yields an internal network.
func TestDockerHardeningIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	rt, err := runtime.NewDockerRuntime(q, testsupport.DiscardLogger(), "")
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	mgr := runtime.NewManager(rt, q, "127.0.0.1", 31000, 31099)
	cli := dockerClient(t)

	chID := seedContainerChallenge(t, pool, q, "per_team", "static", false, `["/data"]`)
	if _, err := pool.Exec(context.Background(), `UPDATE challenges SET image='traefik/whoami:latest', internal_port=80 WHERE id=$1`, chID); err != nil {
		t.Fatalf("set image: %v", err)
	}
	team := seedTeam(t, q, "H")

	inst, err := mgr.DeployForTeam(context.Background(), runtime.DeployReq{
		ChallengeID: chID, TeamID: team, Flag: "OSCTF{static_placeholder}",
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	t.Cleanup(func() { _ = mgr.DestroyInstance(context.Background(), inst.ID) })

	insp := containerForInstance(t, cli, inst.ID)
	hc := insp.HostConfig
	if !hc.ReadonlyRootfs {
		t.Error("ReadonlyRootfs = false, want true")
	}
	if _, ok := hc.Tmpfs["/tmp"]; !ok {
		t.Errorf("Tmpfs missing /tmp: %v", hc.Tmpfs)
	}
	if _, ok := hc.Tmpfs["/data"]; !ok {
		t.Errorf("Tmpfs missing /data (writable_paths): %v", hc.Tmpfs)
	}
	if !containsStr(hc.CapDrop, "ALL") {
		t.Errorf("CapDrop = %v, want ALL", hc.CapDrop)
	}
	if !containsStr(hc.SecurityOpt, "no-new-privileges:true") {
		t.Errorf("SecurityOpt = %v, want no-new-privileges", hc.SecurityOpt)
	}

	// The per-team network exists and is internal (egress off).
	netName := ""
	for n := range insp.NetworkSettings.Networks {
		if strings.HasPrefix(n, "osctf-team-") {
			netName = n
		}
	}
	if netName == "" {
		t.Fatalf("container not on a per-team network: %v", insp.NetworkSettings.Networks)
	}
	ni, err := cli.NetworkInspect(context.Background(), netName, network.InspectOptions{})
	if err != nil {
		t.Fatalf("network inspect: %v", err)
	}
	if !ni.Internal {
		t.Errorf("network %s Internal = false, want true (egress:false)", netName)
	}
}

// TestDockerPerTeamIsolationIntegration proves team A's container cannot reach
// team B's container over the Docker network, with a positive control.
func TestDockerPerTeamIsolationIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	rt, err := runtime.NewDockerRuntime(q, testsupport.DiscardLogger(), "")
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	mgr := runtime.NewManager(rt, q, "127.0.0.1", 31100, 31199)
	cli := dockerClient(t)
	ctx := context.Background()

	// egress:false gives each team an --internal bridge with no route off-network:
	// the ironclad, host-independent isolation guarantee. (For egress:true the same
	// isolation holds on native-Linux Docker via DOCKER-ISOLATION iptables rules,
	// but that is host-dependent — Docker Desktop's VM does not enforce it — so we
	// assert the deterministic internal-network path here.)
	chID := seedContainerChallenge(t, pool, q, "per_team", "static", false, "")
	// Use whoami (listens on 80) as the challenge image.
	if _, err := pool.Exec(ctx, `UPDATE challenges SET image='traefik/whoami:latest', internal_port=80 WHERE id=$1`, chID); err != nil {
		t.Fatalf("set image: %v", err)
	}
	teamA := seedTeam(t, q, "A")
	teamB := seedTeam(t, q, "B")

	instA, err := mgr.DeployForTeam(ctx, runtime.DeployReq{ChallengeID: chID, TeamID: teamA, Flag: "x"})
	if err != nil {
		t.Fatalf("deploy A: %v", err)
	}
	t.Cleanup(func() { _ = mgr.DestroyInstance(ctx, instA.ID) })
	instB, err := mgr.DeployForTeam(ctx, runtime.DeployReq{ChallengeID: chID, TeamID: teamB, Flag: "x"})
	if err != nil {
		t.Fatalf("deploy B: %v", err)
	}
	t.Cleanup(func() { _ = mgr.DestroyInstance(ctx, instB.ID) })

	inspA := containerForInstance(t, cli, instA.ID)
	inspB := containerForInstance(t, cli, instB.ID)
	netA := teamNetOf(t, inspA)
	netB := teamNetOf(t, inspB)
	if netA == netB {
		t.Fatalf("teams share network %q", netA)
	}
	ipB := inspB.NetworkSettings.Networks[netB].IPAddress
	if ipB == "" {
		t.Fatalf("no IP for team B container on %s", netB)
	}

	pullImage(t, cli, "busybox:latest")

	// Positive control: a prober on B's network CAN reach B:80.
	if rc := probe(t, cli, netB, ipB, "80"); rc != 0 {
		t.Fatalf("positive control failed: prober on B's net could not reach B:80 (rc=%d)", rc)
	}
	// Isolation: a prober on A's network CANNOT reach B:80.
	if rc := probe(t, cli, netA, ipB, "80"); rc == 0 {
		t.Errorf("ISOLATION BREACH: team A's network reached team B's container %s:80", ipB)
	}
}

// TestDockerTeamNetworkGCIntegration checks Reconcile removes an empty team net.
func TestDockerTeamNetworkGCIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	rt, err := runtime.NewDockerRuntime(q, testsupport.DiscardLogger(), "")
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	mgr := runtime.NewManager(rt, q, "127.0.0.1", 31200, 31299)
	cli := dockerClient(t)
	ctx := context.Background()

	chID := seedContainerChallenge(t, pool, q, "per_team", "static", true, "")
	if _, err := pool.Exec(ctx, `UPDATE challenges SET image='traefik/whoami:latest', internal_port=80 WHERE id=$1`, chID); err != nil {
		t.Fatalf("set image: %v", err)
	}
	team := seedTeam(t, q, "G")

	inst, err := mgr.DeployForTeam(ctx, runtime.DeployReq{ChallengeID: chID, TeamID: team, Flag: "x"})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	netName := teamNetOf(t, containerForInstance(t, cli, inst.ID))

	// Destroy the only instance, then reconcile → the empty net is GC'd.
	if err := mgr.DestroyInstance(ctx, inst.ID); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if err := mgr.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	nets, err := cli.NetworkList(ctx, network.ListOptions{Filters: filters.NewArgs(filters.Arg("name", netName))})
	if err != nil {
		t.Fatalf("network list: %v", err)
	}
	for _, n := range nets {
		if n.Name == netName {
			t.Errorf("empty team network %s not GC'd", netName)
		}
	}
}

func teamNetOf(t *testing.T, insp container.InspectResponse) string {
	t.Helper()
	for n := range insp.NetworkSettings.Networks {
		if strings.HasPrefix(n, "osctf-team-") {
			return n
		}
	}
	t.Fatalf("container not on a per-team network: %v", insp.NetworkSettings.Networks)
	return ""
}

func pullImage(t *testing.T, cli *client.Client, ref string) {
	t.Helper()
	if _, err := cli.ImageInspect(context.Background(), ref); err == nil {
		return
	}
	rc, err := cli.ImagePull(context.Background(), ref, image.PullOptions{})
	if err != nil {
		t.Fatalf("pull %s: %v", ref, err)
	}
	defer func() { _ = rc.Close() }()
	_, _ = io.Copy(io.Discard, rc)
}

// probe runs a throwaway busybox on the given network attempting a TCP connect to
// ip:port; returns the container exit code (0 = connected/reachable).
func probe(t *testing.T, cli *client.Client, netName, ip, port string) int {
	t.Helper()
	ctx := context.Background()
	created, err := cli.ContainerCreate(ctx,
		&container.Config{Image: "busybox:latest", Cmd: []string{"nc", "-w", "3", ip, port}},
		&container.HostConfig{NetworkMode: container.NetworkMode(netName)},
		nil, nil, "")
	if err != nil {
		t.Fatalf("prober create: %v", err)
	}
	defer func() { _ = cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true}) }()
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		t.Fatalf("prober start: %v", err)
	}
	waitCh, errCh := cli.ContainerWait(ctx, created.ID, container.WaitConditionNotRunning)
	select {
	case werr := <-errCh:
		t.Fatalf("prober wait: %v", werr)
	case res := <-waitCh:
		return int(res.StatusCode)
	case <-time.After(20 * time.Second):
		t.Fatalf("prober timed out")
	}
	return -1
}
