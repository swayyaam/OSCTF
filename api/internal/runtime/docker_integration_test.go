//go:build dockerint

// These tests need a real Docker daemon. Run with: go test -tags dockerint ./internal/runtime/...
package runtime_test

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/testsupport"
)

func TestDockerRuntimeIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	log := testsupport.DiscardLogger()

	rt, err := runtime.NewDockerRuntime(q, log, "")
	if err != nil {
		t.Fatalf("docker runtime: %v", err)
	}
	mgr := runtime.NewManager(rt, q, "127.0.0.1", 30500, 30599)
	assertNoResidue(t, mgr, dockerClient(t)) // Phase 6: no Docker resource residue survives this test

	// Seed a container challenge backed by a tiny public image that listens on 80.
	chID := uuid.Must(uuid.NewV7())
	img := "traefik/whoami:latest"
	port := int32(80)
	if _, err := q.CreateChallenge(context.Background(), gen.CreateChallengeParams{
		ID: chID, Slug: "whoami-" + chID.String()[:8], Title: "Whoami", Category: "web",
		Kind: "container", Flag: "OSCTF{docker}", Scoring: "static", PointsInitial: 100,
		Image: &img, InternalPort: &port, MemLimitMb: 128, CpuMillis: 500,
		ContainerEnv: []byte("{}"), Visible: true,
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}

	// Deploy → running, TCP reachable on the allocated port.
	inst, err := mgr.Deploy(context.Background(), chID)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Destroy(context.Background(), chID) })

	if inst.State != runtime.StateRunning {
		t.Fatalf("state = %q, want running", inst.State)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(inst.HostPort))
	deadline := time.Now().Add(15 * time.Second)
	var reachable bool
	for time.Now().Before(deadline) {
		if c, derr := net.DialTimeout("tcp", addr, time.Second); derr == nil {
			_ = c.Close()
			reachable = true
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !reachable {
		t.Fatalf("container port %s not reachable", addr)
	}

	// Logs endpoint returns output.
	if _, err := mgr.Logs(context.Background(), chID, 50); err != nil {
		t.Errorf("logs: %v", err)
	}

	// Destroy frees the port; a redeploy gets the same lowest port.
	if err := mgr.Destroy(context.Background(), chID); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	inst2, err := mgr.Deploy(context.Background(), chID)
	if err != nil {
		t.Fatalf("redeploy: %v", err)
	}
	if inst2.HostPort != 30500 {
		t.Errorf("redeploy port = %d, want 30500 (freed)", inst2.HostPort)
	}
}
