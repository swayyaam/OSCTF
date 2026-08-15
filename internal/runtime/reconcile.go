package runtime

import "time"

// This file holds the reconcile DECISION as a pure function: given what the daemon
// shows (containers, networks) and what the DB believes (rows), plus the current
// time, it returns the list of Actions to apply. It performs no Docker calls, no DB
// calls, and reads no clock — everything it needs is an argument. DockerRuntime and
// FakeRuntime both gather → call Reconcile → execute, so the decision is exercised by
// table tests without a daemon (docs/v0.2/03-runtime.md).

// reconcileGrace is how long after a row's last change reconcile leaves it alone, to
// avoid acting on a row whose deploy/transition is still in flight (the container may
// not be listed yet, or its id not recorded). Derived from the deploy timeout plus a
// margin IN CODE so the two can never drift apart when one is tuned (2a-i backstop).
const reconcileGrace = deployTimeout + 30*time.Second

// ContainerView is the reconcile-relevant projection of one osctf.managed container.
type ContainerView struct {
	ContainerID string
	// InstanceID is the osctf.instance_id label. Empty when the label is missing or
	// unparseable — such a container is never removed (we cannot identify it) but is
	// flagged unadopted so it does not hold its port invisibly.
	InstanceID string
	Running    bool
}

// InstanceRow is the reconcile-relevant projection of one DB instance row.
type InstanceRow struct {
	InstanceID string
	TeamID     string // "" for shared instances
	State      string
	// ContainerID is the container the row believes it owns; empty when the deploy
	// has not written it yet (still creating the container).
	ContainerID string
	UpdatedAt   time.Time
}

// NetworkView is the reconcile-relevant projection of one osctf.managed network.
type NetworkView struct {
	NetworkID   string
	Name        string
	TeamID      string // owning team (osctf.team_id label); "" if unlabeled/legacy
	TeamNetwork bool   // per-team bridge (osctf.team_network label / osctf-team- prefix)
	Attached    int    // number of attached containers
}

// UnadoptedContainer is a managed container reconcile could not resolve to an
// instance (missing/unparseable osctf.instance_id) — surfaced in the admin fleet
// view so an organizer can see a container silently holding a host port.
type UnadoptedContainer struct {
	ContainerID string
	Image       string
	CreatedAt   time.Time
}

// UnadoptedNetwork is a per-team bridge with no resolvable team_id (e.g. from a
// pre-team_id-label release). Never GC'd; surfaced for manual cleanup.
type UnadoptedNetwork struct {
	NetworkID string
	Name      string
}

// ActionKind enumerates the reconcile decisions.
type ActionKind int

const (
	// ActionMarkLost sets a row to 'lost' because its container has vanished.
	ActionMarkLost ActionKind = iota
	// ActionRemoveContainer removes a managed container that no live row owns (an
	// orphan) or that a row has superseded (a stale container from an older spec).
	ActionRemoveContainer
	// ActionRemoveNetwork removes an empty per-team bridge.
	ActionRemoveNetwork
	// ActionFlagUnadopted reports a managed container whose instance_id label is
	// missing/unparseable — never removed, but surfaced (metric, log, fleet view) so
	// an organizer sees a container silently holding a port.
	ActionFlagUnadopted
	// ActionFlagUnadoptedNetwork reports a team bridge with no resolvable team_id
	// (e.g. created by a pre-team_id-label release). Never GC'd — without the team we
	// cannot tell an in-flight deploy from an abandoned bridge — but surfaced like an
	// unadopted container so an organizer can clean it up.
	ActionFlagUnadoptedNetwork
)

// ReconcileStats reports what a pass saw, for observability: it must be visible when
// a pass skips everything (e.g. clock skew) or when it silently does nothing.
type ReconcileStats struct {
	SkippedGrace int // rows left alone because within grace (includes FutureRows)
	FutureRows   int // rows whose updated_at is AHEAD of now — a clock-skew anomaly
}

// metricLabel is the stable label for the per-kind action counter.
func (k ActionKind) metricLabel() string {
	switch k {
	case ActionMarkLost:
		return "mark_lost"
	case ActionRemoveContainer:
		return "remove_container"
	case ActionRemoveNetwork:
		return "remove_network"
	case ActionFlagUnadopted:
		return "flag_unadopted"
	case ActionFlagUnadoptedNetwork:
		return "flag_unadopted_network"
	default:
		return "unknown"
	}
}

// Action is one reconcile decision. Which id field is set depends on Kind:
// MarkLost→InstanceID, RemoveContainer/FlagUnadopted→ContainerID, RemoveNetwork→NetworkID.
type Action struct {
	Kind        ActionKind
	InstanceID  string
	ContainerID string
	NetworkID   string
	Reason      string // for logging/audit
}

// Reconcile decides how to align the daemon's observed state with the DB rows.
//
// Division of labour with the reaper (ReapStaleOnce, which deletes pending/error
// rows past ReapAfter): reconcile NEVER touches a pending row and never deletes a
// row — the two operate on disjoint states, so a slow deploy's pending row is only
// ever reaped (age-gated, ReapAfter ≫ deploy timeout), never marked lost mid-deploy.
// Two guards keep a still-recording deploy safe: a row updated within reconcileGrace
// is skipped entirely, and a row with no committed ContainerID never has its
// container removed regardless of age. Team-network GC is likewise held off while
// that team has a pending or fresh row.
func Reconcile(observed []ContainerView, rows []InstanceRow, networks []NetworkView, now time.Time) ([]Action, ReconcileStats) {
	var actions []Action
	var stats ReconcileStats

	byInstance := make(map[string]ContainerView, len(observed))
	for _, c := range observed {
		if c.InstanceID != "" {
			byInstance[c.InstanceID] = c
		}
	}
	rowByID := make(map[string]InstanceRow, len(rows))
	protectedTeams := make(map[string]bool)
	for _, r := range rows {
		rowByID[r.InstanceID] = r
		if r.TeamID != "" && (State(r.State) == StatePending || now.Sub(r.UpdatedAt) < reconcileGrace) {
			protectedTeams[r.TeamID] = true
		}
	}

	// Rows with no container at all → mark lost (only states that should have one).
	// Fresh rows are left alone (deploy/transition may be in flight). A row whose
	// updated_at is AHEAD of now is a clock-skew anomaly: still conservatively skipped
	// (counted in SkippedGrace) but also counted in FutureRows so it is not silent.
	for _, r := range rows {
		age := now.Sub(r.UpdatedAt)
		if age < 0 {
			stats.FutureRows++
		}
		if age < reconcileGrace {
			stats.SkippedGrace++
			continue
		}
		if _, hasContainer := byInstance[r.InstanceID]; hasContainer {
			continue
		}
		switch State(r.State) {
		case StateRunning, StateStarting, StateUnhealthy:
			actions = append(actions, Action{
				Kind: ActionMarkLost, InstanceID: r.InstanceID, Reason: "container vanished",
			})
		default:
			// pending → reaper's domain (never marked lost here); error/stopped/lost →
			// terminal. Nothing to do.
		}
	}

	// Containers: unresolvable id → flag; no row → orphan remove; row present but a
	// different container than the row committed → stale, remove.
	for _, c := range observed {
		if c.InstanceID == "" {
			actions = append(actions, Action{
				Kind: ActionFlagUnadopted, ContainerID: c.ContainerID,
				Reason: "managed container with missing/unparseable instance_id label",
			})
			continue
		}
		r, tracked := rowByID[c.InstanceID]
		if !tracked {
			actions = append(actions, Action{
				Kind: ActionRemoveContainer, ContainerID: c.ContainerID,
				Reason: "orphan container (no row) for instance " + c.InstanceID,
			})
			continue
		}
		if now.Sub(r.UpdatedAt) < reconcileGrace || r.ContainerID == "" {
			continue // fresh, or the deploy has not recorded its container id yet — never remove
		}
		if r.ContainerID != c.ContainerID {
			actions = append(actions, Action{
				Kind: ActionRemoveContainer, ContainerID: c.ContainerID,
				Reason: "stale container superseded for instance " + c.InstanceID,
			})
		}
	}

	// Empty per-team bridges → GC, unless that team has a pending/fresh row (its
	// network is created before the container, so an in-flight deploy shows
	// Attached==0). A bridge with no resolvable team_id is never GC'd (we cannot tell
	// an in-flight deploy from an abandoned one) — it is flagged unadopted instead.
	for _, n := range networks {
		if !n.TeamNetwork || n.Attached != 0 {
			continue
		}
		if n.TeamID == "" {
			actions = append(actions, Action{
				Kind: ActionFlagUnadoptedNetwork, NetworkID: n.NetworkID,
				Reason: "team network with no resolvable team_id " + n.Name,
			})
			continue
		}
		if !protectedTeams[n.TeamID] {
			actions = append(actions, Action{
				Kind: ActionRemoveNetwork, NetworkID: n.NetworkID,
				Reason: "empty team network " + n.Name,
			})
		}
	}

	return actions, stats
}
