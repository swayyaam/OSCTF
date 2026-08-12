package plugintest_test

import "testing"

// Pending-invariant MANIFEST — the spec invariants (docs/v0.3/03-plugin-loader.md, "Invariants
// the tests pin") not yet pinned by a real test, each tagged with the sub-step that unblocks
// it. Written from the spec before the loader exists, so the loader is built to satisfy these
// (P3-c's framing: make them pass one at a time), not the tests shaped to match it.
//
// THIS IS A MANIFEST, NOT COVERAGE. The entries below are DATA, not tests — nothing here
// exercises an invariant, and nothing counts them as tests that ran. The only test in this
// file is the self-emptying guard, which is a real, passing test WHILE entries are legitimately
// pending and FAILS the moment a landed phase still lists an unimplemented invariant. That is
// the forcing function: a "what isn't tested yet" list is only useful while something shrinks it.
//
// Pinned NOW lives in doubles_test.go (transport + subprocess doubles). Inv 12 (reader-atomic
// registry swap) is already pinned by P1's registry contention tests. Inv 2 (non-ready never
// serves) is pinned by state_test.go + routing_test.go; and the rest of P3-c by
// supervisor_test.go — Inv 4 (crash-loop cap/quarantine, the crash-detection gap, the
// sustained-health reset), Inv 5 (reload idempotent — process replaced, old reaped, one entry,
// no leak, converges under concurrency), Inv 7 (full-stop cleanup — goleak + reap + fd count
// over load→serve→stop), and Inv 1 (boot orphan-sweep — a Pdeathsig-disabled child survives a
// host SIGKILL and only the sweep reclaims it, refusing a reused pid). P3-c is complete. Inv 11
// (two-level in-flight budget, shed-names-cap, isolation, tail histogram, drain-timeout release)
// is pinned by inflight_test.go, plus cmd/platform/budget_test.go for the shared fd accountant
// wired into the composition root. P3-d is complete.

type pendingInvariant struct {
	id     string // spec invariant number + short name
	phase  string // the sub-step that unblocks it (see knownPhases)
	reason string // why it can't be written even as a failing test yet
}

var knownPhases = map[string]bool{"P3-c": true, "P3-d": true, "P3-e": true}

// completedPhases marks the sub-steps whose loader/isolation/wiring now EXISTS. When a phase
// lands, add it here — the guard then fails for every entry still tagged with it, forcing each
// to become a real passing test and be DELETED from pendingInvariants. This IS P3-c's exit
// gate: flip "P3-c" on, migrate every P3-c entry to a real test + remove it, green again.
var completedPhases = map[string]bool{
	"P3-c": true, // loader lifecycle landed: state machine + routing (#2), crash-loop
	// quarantine (#4), reload (#5), full-stop cleanup (#7), boot orphan-sweep (#1) — all pinned
	// by real tests in the plugin package; no P3-c entry may remain below.
	"P3-d": true, // in-flight budget landed (#11): two-level cap + shed-names-cap + isolation +
	// tail histogram + drain-timeout release (inflight_test.go), and the shared fd accountant
	// wired into the composition root (cmd/platform budget_test.go asserts the reserve is counted
	// exactly once). No P3-d entry may remain below.
	"P3-e": true, // plugins wired into real requests: boot/drain + registry register-on-ready/
	// revert-before-death (#3), challenge-type in submission (#8), plugin scoring locked-at-solve
	// off the read path (#9), notification bus (#10). The full-server "boot does not gate serving"
	// assertion is now pinned by cmd/platform TestBootDoesNotGateServing (real loader + real
	// httpserver + a never-ready nohandshake subprocess → /healthz 200). No P3-e entry may remain.
}

// pendingInvariants is EMPTY: every v0.3 plugin-loader spec invariant is now pinned by a real test.
// The self-emptying guard below stays as the forcing function for any FUTURE phase that adds
// entries — an empty manifest is the goal state, not the absence of the mechanism.
var pendingInvariants = []pendingInvariant{}

// TestPendingInvariantsSelfEmpty forces the manifest to shrink. It also catches a typo'd phase
// (one that could never be marked complete and so would never be forced out).
func TestPendingInvariantsSelfEmpty(t *testing.T) {
	for _, p := range pendingInvariants {
		if !knownPhases[p.phase] {
			t.Errorf("pending invariant %q has unknown phase %q — use one of P3-c/P3-d/P3-e", p.id, p.phase)
			continue
		}
		if completedPhases[p.phase] {
			t.Errorf("invariant %q is still pending but %s has LANDED — implement it as a real passing test and delete this manifest entry (%s)",
				p.id, p.phase, p.reason)
		}
	}
}
