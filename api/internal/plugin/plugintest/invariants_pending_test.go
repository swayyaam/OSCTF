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
// serves) is pinned by state_test.go + routing_test.go; Inv 4 (crash-loop cap/quarantine, the
// crash-detection gap, and the sustained-health reset), Inv 5 (reload idempotent — process
// replaced, old reaped, one entry, no leak, converges under concurrency), and Inv 7 (full-stop
// cleanup — goleak + reap + fd count over load→serve→stop) by supervisor_test.go — all in the
// plugin package (P3-c).

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
	// "P3-c": true,
}

var pendingInvariants = []pendingInvariant{
	{"1 boot orphan-sweep", "P3-c", "needs the loader's boot reconciliation (pidfile sweep); graceful-reap half is pinned now"},
	{"3 registry-never-holds-stopped", "P3-e", "needs loader stop AND registry wiring (revert-before-death)"},
	{"8 challenge-type attempt untouched", "P3-e", "needs challenge-type registry + submissions tx"},
	{"9 scoreboard recomputable", "P3-e", "needs scoring wiring + scoreboard (fallback off/on)"},
	{"10 notification-drop observable", "P3-e", "needs the event bus"},
	{"11 in-flight budget + latency metric", "P3-d", "needs the call wrapper + OSCTF_PLUGIN_MAX_INFLIGHT semaphore"},
}

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
