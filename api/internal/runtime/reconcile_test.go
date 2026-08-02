package runtime

import (
	"sort"
	"testing"
	"time"
)

// summarize renders actions into a stable, comparable form (kind + the id that kind
// carries), so table cases can assert on decisions without depending on order.
func summarize(actions []Action) []string {
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		switch a.Kind {
		case ActionMarkLost:
			out = append(out, "MarkLost:"+a.InstanceID)
		case ActionRemoveContainer:
			out = append(out, "RemoveContainer:"+a.ContainerID)
		case ActionRemoveNetwork:
			out = append(out, "RemoveNetwork:"+a.NetworkID)
		case ActionFlagUnadopted:
			out = append(out, "FlagUnadopted:"+a.ContainerID)
		case ActionFlagUnadoptedNetwork:
			out = append(out, "FlagUnadoptedNetwork:"+a.NetworkID)
		}
	}
	sort.Strings(out)
	return out
}

func TestReconcileDecisions(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour)      // well past reconcileGrace
	fresh := now.Add(-10 * time.Second) // within reconcileGrace

	cases := []struct {
		name     string
		observed []ContainerView
		rows     []InstanceRow
		networks []NetworkView
		want     []string
	}{
		{
			name: "running row with no container is lost",
			rows: []InstanceRow{{InstanceID: "i1", State: "running", UpdatedAt: old}},
			want: []string{"MarkLost:i1"},
		},
		{
			name: "pending row with no container is left to the reaper",
			rows: []InstanceRow{{InstanceID: "i1", State: "pending", UpdatedAt: old}},
			want: nil,
		},
		{
			name:     "labelled container with no row is an orphan",
			observed: []ContainerView{{ContainerID: "c1", InstanceID: "i1"}},
			want:     []string{"RemoveContainer:c1"},
		},
		{
			name:     "container and row in sync is zero actions",
			observed: []ContainerView{{ContainerID: "c1", InstanceID: "i1", Running: true}},
			rows:     []InstanceRow{{InstanceID: "i1", State: "running", ContainerID: "c1", UpdatedAt: old}},
			want:     nil,
		},
		{
			name:     "exited container with a matching row is left to health grading",
			observed: []ContainerView{{ContainerID: "c1", InstanceID: "i1", Running: false}},
			rows:     []InstanceRow{{InstanceID: "i1", State: "running", ContainerID: "c1", UpdatedAt: old}},
			want:     nil, // pure Reconcile makes no decision; refreshHealth grades it
		},
		{
			name:     "stale container the row superseded is removed",
			observed: []ContainerView{{ContainerID: "c_old", InstanceID: "i1"}},
			rows:     []InstanceRow{{InstanceID: "i1", State: "running", ContainerID: "c_new", UpdatedAt: old}},
			want:     []string{"RemoveContainer:c_old"},
		},
		{
			name:     "unadopted container (missing/unparseable id) is flagged, not removed",
			observed: []ContainerView{{ContainerID: "c1", InstanceID: ""}},
			want:     []string{"FlagUnadopted:c1"},
		},
		{
			name:     "empty team network is garbage-collected",
			networks: []NetworkView{{NetworkID: "n1", Name: "osctf-team-abcd", TeamNetwork: true, TeamID: "teamA", Attached: 0}},
			want:     []string{"RemoveNetwork:n1"},
		},
		// 2b-2-ii: a team bridge with no resolvable team_id (pre-team_id-label release)
		// must never be GC'd — flag it instead.
		{
			name:     "empty team network with no team_id is flagged, never GC'd",
			networks: []NetworkView{{NetworkID: "n1", Name: "osctf-team-abcd", TeamNetwork: true, TeamID: "", Attached: 0}},
			want:     []string{"FlagUnadoptedNetwork:n1"},
		},
		// 2e v0.2-upgrade case: a container carrying the old label set is adopted
		// normally (its instance_id label is unchanged), while its network — which
		// predates the team_id label — is flagged, not destroyed.
		{
			name:     "v0.2 upgrade: labelled container adopted, its no-team_id network not destroyed",
			observed: []ContainerView{{ContainerID: "c1", InstanceID: "i1", Running: true}},
			rows:     []InstanceRow{{InstanceID: "i1", State: "running", ContainerID: "c1", UpdatedAt: old}},
			networks: []NetworkView{{NetworkID: "n1", Name: "osctf-team-abcd", TeamNetwork: true, TeamID: "", Attached: 0}},
			want:     []string{"FlagUnadoptedNetwork:n1"}, // container: zero actions (adopted); network: flagged
		},
		// 2a-i: a deploy that used its full budget (row older than grace) but has not
		// yet recorded its container id must NOT have its container removed.
		{
			name:     "row present with empty container id, container present, old → zero actions",
			observed: []ContainerView{{ContainerID: "c1", InstanceID: "i1"}},
			rows:     []InstanceRow{{InstanceID: "i1", State: "pending", ContainerID: "", UpdatedAt: old}},
			want:     nil,
		},
		// 2a-ii: an in-flight deploy's team network is empty (created before the
		// container); a pending row for that team must hold off GC.
		{
			name:     "empty team network with a pending row for that team is not GC'd",
			networks: []NetworkView{{NetworkID: "n1", Name: "osctf-team-abcd", TeamNetwork: true, TeamID: "teamA", Attached: 0}},
			rows:     []InstanceRow{{InstanceID: "i1", TeamID: "teamA", State: "pending", UpdatedAt: old}},
			want:     nil,
		},
		{
			name:     "fresh row within grace is skipped entirely",
			observed: []ContainerView{{ContainerID: "c_old", InstanceID: "i1"}},
			rows:     []InstanceRow{{InstanceID: "i1", State: "running", ContainerID: "c_new", UpdatedAt: fresh}},
			want:     nil, // would be a stale-remove if not fresh
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actions, _ := Reconcile(tc.observed, tc.rows, tc.networks, now)
			got := summarize(actions)
			want := tc.want
			if want == nil {
				want = []string{}
			}
			sort.Strings(want)
			if len(got) != len(want) {
				t.Fatalf("actions = %v, want %v", got, want)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("actions = %v, want %v", got, want)
				}
			}
		})
	}
}

// TestReconcileActionOrderContainersBeforeNetworks: the executor applies actions in
// slice order, so a container must be scheduled for removal BEFORE the network it may
// sit on — otherwise NetworkRemove fails with "has active endpoints". Reconcile emits
// container actions before network actions.
func TestReconcileActionOrderContainersBeforeNetworks(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	old := now.Add(-1 * time.Hour)
	observed := []ContainerView{{ContainerID: "c1", InstanceID: "i1"}} // orphan → remove
	networks := []NetworkView{{NetworkID: "n1", Name: "osctf-team-x", TeamNetwork: true, TeamID: "teamA", Attached: 0}}
	actions, _ := Reconcile(observed, nil, networks, old)

	var containerIdx, networkIdx = -1, -1
	for i, a := range actions {
		if a.Kind == ActionRemoveContainer {
			containerIdx = i
		}
		if a.Kind == ActionRemoveNetwork {
			networkIdx = i
		}
	}
	if containerIdx < 0 || networkIdx < 0 {
		t.Fatalf("expected both a container and a network removal, got %v", summarize(actions))
	}
	if containerIdx > networkIdx {
		t.Errorf("container removal (idx %d) must come before network removal (idx %d)", containerIdx, networkIdx)
	}
}

// TestReconcileFutureRowIsConservativeAndCounted covers the clock-skew anomaly: a row
// whose updated_at is AHEAD of now (app host lagging the DB) must be treated as an
// anomaly — the row is skipped conservatively (no action) AND surfaced via the stats
// counter, not silently ignored.
func TestReconcileFutureRowIsConservativeAndCounted(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	rows := []InstanceRow{
		{InstanceID: "i1", State: "running", UpdatedAt: now.Add(30 * time.Second)}, // future
	}
	actions, stats := Reconcile(nil, rows, nil, now)
	if len(actions) != 0 {
		t.Fatalf("future-dated row produced actions %v, want none (conservative)", summarize(actions))
	}
	if stats.FutureRows != 1 {
		t.Errorf("FutureRows = %d, want 1 (anomaly must be counted, not silent)", stats.FutureRows)
	}
	if stats.SkippedGrace != 1 {
		t.Errorf("SkippedGrace = %d, want 1", stats.SkippedGrace)
	}
}
