package plugin

import "testing"

// The loader's state machine is pinned before the loader is built (docs/v0.3/03). Every
// plugin is in exactly one state; only the transitions in the spec table are legal, and
// ONLY `ready` serves new requests. These are the foundation the routing invariant
// (non-ready-never-serves) and the supervision invariants (quarantine, reload, drain)
// build on.

func TestStateMachineLegalTransitions(t *testing.T) {
	// The spec table (docs/v0.3/03-plugin-loader.md → "Legal transitions"). Anything not
	// listed here must be rejected.
	legal := map[State][]State{
		StateDiscovered: {StateLaunching, StateFailed}, // failed: quarantined at load (invalid config), never launched
		StateLaunching:  {StateReady, StateRestarting, StateFailed},
		StateReady:      {StateUnhealthy, StateDraining},
		StateUnhealthy:  {StateReady, StateRestarting, StateDraining},
		StateRestarting: {StateLaunching, StateFailed},
		StateFailed:     {StateLaunching}, // the ONLY exit from quarantine — explicit reload
		StateDraining:   {StateStopped},
		StateStopped:    {}, // terminal
	}

	all := []State{
		StateDiscovered, StateLaunching, StateReady, StateUnhealthy,
		StateRestarting, StateFailed, StateDraining, StateStopped,
	}
	for _, from := range all {
		for _, to := range all {
			wantLegal := false
			for _, l := range legal[from] {
				if l == to {
					wantLegal = true
				}
			}
			m := &machine{state: from}
			err := m.to(to)
			switch {
			case wantLegal && err != nil:
				t.Errorf("%s -> %s should be legal, got error %v", from, to, err)
			case wantLegal && m.state != to:
				t.Errorf("%s -> %s legal but state stayed %s", from, to, m.state)
			case !wantLegal && err == nil:
				t.Errorf("%s -> %s should be ILLEGAL, but the machine allowed it (now %s)", from, to, m.state)
			case !wantLegal && m.state != from:
				t.Errorf("%s -> %s illegal but the machine moved to %s anyway", from, to, m.state)
			}
		}
	}
}

func TestOnlyReadyServesNew(t *testing.T) {
	for _, st := range []State{
		StateDiscovered, StateLaunching, StateUnhealthy,
		StateRestarting, StateFailed, StateDraining, StateStopped,
	} {
		if (&machine{state: st}).servesNew() {
			t.Errorf("state %q must not serve new requests", st)
		}
	}
	if !(&machine{state: StateReady}).servesNew() {
		t.Error("state ready must serve new requests")
	}
}
