//go:build dockerint

// Real-daemon tests for the v0.2 hardening pass and per-team network isolation.
// Run with: go test -tags dockerint ./internal/runtime/...
package runtime_test

import (
	"context"
	"io"
	"os"
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

	// An egress:false container must STILL publish its host port. Docker drops
	// port bindings on an `internal` network, which silently made every
	// egress:false challenge unreachable; assert the binding took effect rather
	// than merely that we asked for it.
	if len(insp.NetworkSettings.Ports) == 0 {
		t.Error("NetworkSettings.Ports is empty: the host port was never published")
	}
	for port, binds := range insp.NetworkSettings.Ports {
		if len(binds) == 0 {
			t.Errorf("port %s has no host binding: unreachable to players", port)
		}
	}

	// The per-team network exists and has outbound NAT disabled (egress off).
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
	if ni.Internal {
		t.Errorf("network %s is internal: that voids published ports", netName)
	}
	if got := ni.Options["com.docker.network.bridge.enable_ip_masquerade"]; got != "false" {
		t.Errorf("network %s ip_masquerade = %q, want \"false\" (egress:false)", netName, got)
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

	// Per-team instances sit on their own bridges AND publish a host port. Native
	// Linux Docker still blocks cross-bridge traffic to the container IP (via the
	// DOCKER-ISOLATION chains) — verified on a real Linux daemon. Docker Desktop's
	// VM does NOT once a port is published, so this assertion is gated below:
	// strict on daemons expected to enforce it (CI/Linux, OSCTF_ISOLATION_ENFORCED),
	// an informative skip otherwise (the platform logs a startup warning either way).
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
		if os.Getenv("OSCTF_ISOLATION_ENFORCED") != "" {
			t.Errorf("ISOLATION BREACH: team A's network reached team B's container %s:80", ipB)
		} else {
			t.Skipf("this daemon does not enforce cross-network isolation for published-port "+
				"containers (expected on Docker Desktop; the platform logs a startup warning). "+
				"Set OSCTF_ISOLATION_ENFORCED=1 to require it — CI does, on native Linux. (reached %s:80)", ipB)
		}
	}
}

// TestVerifyIsolationSelfCheckIntegration exercises the platform's startup
// isolation self-check against the real daemon. Its verdict must match reality:
// where the daemon is expected to enforce isolation (OSCTF_ISOLATION_ENFORCED,
// set by CI on Linux) the check must report isolated=true. Elsewhere it may
// report false (correctly detecting a non-enforcing daemon like Docker Desktop),
// which is what drives the loud startup warning.
func TestVerifyIsolationSelfCheckIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	rt, err := runtime.NewDockerRuntime(q, testsupport.DiscardLogger(), "")
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	mgr := runtime.NewManager(rt, q, "127.0.0.1", 31300, 31399)

	isolated, err := mgr.VerifyIsolation(context.Background())
	if err != nil {
		t.Skipf("isolation self-check inconclusive on this daemon: %v", err)
	}
	t.Logf("VerifyIsolation: isolated=%v", isolated)
	if !isolated && os.Getenv("OSCTF_ISOLATION_ENFORCED") != "" {
		t.Error("VerifyIsolation reported NOT isolated on a daemon expected to enforce it")
	}
}

// TestDockerEgressBlockedIntegration proves an egress:false challenge cannot
// reach the internet. Asserting the network OPTION is not enough — the option
// only removes the MASQUERADE rule, and whether that actually severs egress is a
// property of the host. Skipped on Docker Desktop, whose VM re-NATs at its own
// boundary and defeats it; CI runs on Linux, where this is the real check.
func TestDockerEgressBlockedIntegration(t *testing.T) {
	cli := dockerClient(t)
	ctx := context.Background()

	info, err := cli.Info(ctx)
	if err != nil {
		t.Fatalf("docker info: %v", err)
	}
	if strings.Contains(info.OperatingSystem, "Docker Desktop") {
		t.Skip("Docker Desktop re-NATs at the VM boundary; egress:false is not enforceable here")
	}

	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	rt, err := runtime.NewDockerRuntime(q, testsupport.DiscardLogger(), "")
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	mgr := runtime.NewManager(rt, q, "127.0.0.1", 31300, 31399)

	// egress:false challenge.
	chIDOff := seedContainerChallenge(t, pool, q, "per_team", "static", false, "")
	if _, err := pool.Exec(ctx, `UPDATE challenges SET image='traefik/whoami:latest', internal_port=80 WHERE id=$1`, chIDOff); err != nil {
		t.Fatalf("set image: %v", err)
	}
	teamOff := seedTeam(t, q, "EgressOff")
	instOff, err := mgr.DeployForTeam(ctx, runtime.DeployReq{ChallengeID: chIDOff, TeamID: teamOff, Flag: "x"})
	if err != nil {
		t.Fatalf("deploy egress-off: %v", err)
	}
	t.Cleanup(func() { _ = mgr.DestroyInstance(ctx, instOff.ID) })

	// egress:true challenge, as the positive control.
	chIDOn := seedContainerChallenge(t, pool, q, "per_team", "static", true, "")
	if _, err := pool.Exec(ctx, `UPDATE challenges SET image='traefik/whoami:latest', internal_port=80 WHERE id=$1`, chIDOn); err != nil {
		t.Fatalf("set image: %v", err)
	}
	teamOn := seedTeam(t, q, "EgressOn")
	instOn, err := mgr.DeployForTeam(ctx, runtime.DeployReq{ChallengeID: chIDOn, TeamID: teamOn, Flag: "x"})
	if err != nil {
		t.Fatalf("deploy egress-on: %v", err)
	}
	t.Cleanup(func() { _ = mgr.DestroyInstance(ctx, instOn.ID) })

	pullImage(t, cli, "busybox:latest")

	// A literal IP, not a hostname: a DNS failure would look like a blocked
	// egress and pass this test for the wrong reason.
	const external, externalPort = "1.1.1.1", "80"

	netOn := teamNetOf(t, containerForInstance(t, cli, instOn.ID))
	if rc := probe(t, cli, netOn, external, externalPort); rc != 0 {
		t.Skipf("positive control failed: this host has no outbound path to %s (rc=%d)", external, rc)
	}
	netOff := teamNetOf(t, containerForInstance(t, cli, instOff.ID))
	if rc := probe(t, cli, netOff, external, externalPort); rc == 0 {
		t.Errorf("EGRESS BREACH: an egress:false challenge reached %s:%s", external, externalPort)
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
