//go:build dockerint

package runtime_test

import (
	"context"
	"testing"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"

	"github.com/osctf/platform/internal/runtime"
)

// assertNoResidue is the Docker-resource residue guard (Phase 6). Registered as a
// t.Cleanup (LIFO — so call it FIRST in a test, before the test's own
// mgr.Destroy* cleanups, so it runs LAST). At cleanup it runs the production GC
// path — mgr.Reconcile, which removes orphaned containers and GCs empty per-team
// bridges — then fails if any osctf-managed container, per-team bridge, or managed
// volume created during the test still survives.
//
// Reconcile IS the path this guards: a per-team bridge is not removed by
// DestroyInstance (an instance may share it); it is GC'd by Reconcile once empty.
// If label handling regresses so Reconcile can't match a resource to its owner,
// that resource becomes unadopted and is never removed — it survives here as
// residue instead of accumulating silently (AGENTS.md flags label handling as
// high-risk). It finds nothing today; that is the point.
//
// dockerint tests run sequentially (no t.Parallel + disjoint port ranges), so
// "present now but not at the baseline" is an accurate per-test run scope without
// a bespoke run-id label. The shared singleton osctf-challenges bridge is
// excluded: it is osctf.managed but carries no osctf.team_network label and is
// meant to persist, so the per-team-bridge filter leaves it out.
func assertNoResidue(t *testing.T, mgr *runtime.Manager, cli *client.Client) {
	t.Helper()
	baseContainers := setOf(managedContainerIDs(t, cli))
	baseNetworks := setOf(teamNetworkIDs(t, cli))
	baseVolumes := setOf(managedVolumeNames(t, cli))

	t.Cleanup(func() {
		// Run the production GC (orphan-container removal + empty-team-network GC)
		// before asserting — residue that survives Reconcile means a resource
		// could not be matched to its owner and will never be cleaned.
		if err := mgr.Reconcile(context.Background()); err != nil {
			t.Errorf("reconcile during residue check: %v", err)
		}
		if leaked := newBeyond(managedContainerIDs(t, cli), baseContainers); len(leaked) > 0 {
			t.Errorf("container residue survived Destroy+Reconcile — orphan-removal / label regression? leaked: %v", leaked)
		}
		if leaked := newBeyond(teamNetworkIDs(t, cli), baseNetworks); len(leaked) > 0 {
			t.Errorf("per-team bridge residue survived Reconcile — network-GC / team_id-label regression? leaked: %v", leaked)
		}
		if leaked := newBeyond(managedVolumeNames(t, cli), baseVolumes); len(leaked) > 0 {
			t.Errorf("managed-volume residue survived cleanup: %v", leaked)
		}
	})
}

const managedLabelSelector = "osctf.managed=true"

func managedContainerIDs(t *testing.T, cli *client.Client) []string {
	t.Helper()
	l, err := cli.ContainerList(context.Background(), container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", managedLabelSelector)),
	})
	if err != nil {
		t.Fatalf("list managed containers: %v", err)
	}
	ids := make([]string, 0, len(l))
	for _, c := range l {
		ids = append(ids, c.ID)
	}
	return ids
}

func teamNetworkIDs(t *testing.T, cli *client.Client) []string {
	t.Helper()
	l, err := cli.NetworkList(context.Background(), network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "osctf.team_network=true")),
	})
	if err != nil {
		t.Fatalf("list per-team networks: %v", err)
	}
	ids := make([]string, 0, len(l))
	for _, n := range l {
		ids = append(ids, n.ID)
	}
	return ids
}

func managedVolumeNames(t *testing.T, cli *client.Client) []string {
	t.Helper()
	l, err := cli.VolumeList(context.Background(), volume.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", managedLabelSelector)),
	})
	if err != nil {
		t.Fatalf("list managed volumes: %v", err)
	}
	names := make([]string, 0, len(l.Volumes))
	for _, v := range l.Volumes {
		names = append(names, v.Name)
	}
	return names
}

func setOf(ids []string) map[string]bool {
	s := make(map[string]bool, len(ids))
	for _, id := range ids {
		s[id] = true
	}
	return s
}

func newBeyond(current []string, base map[string]bool) []string {
	var extra []string
	for _, id := range current {
		if !base[id] {
			extra = append(extra, id)
		}
	}
	return extra
}
