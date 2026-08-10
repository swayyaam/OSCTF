package plugin

import (
	"errors"
	"testing"
)

// Invariant #2 (docs/v0.3/03): a plugin in ANY non-ready state never serves a request —
// dispatch reaches the plugin's process only from `ready`; every other state returns
// ErrNotReady without touching the client, so the type-specific fallback-or-error layered
// above never has to un-ring a call that already hit a dying plugin. Drives every state and
// asserts the call reaches the process iff ready.
func TestDispatchOnlyReachesReadyProcess(t *testing.T) {
	all := []State{
		StateDiscovered, StateLaunching, StateReady, StateUnhealthy,
		StateRestarting, StateFailed, StateDraining, StateStopped,
	}
	for _, st := range all {
		l := &Loader{plugins: map[string]*managed{
			"p": {name: "p", client: "client-handle", m: machine{state: st}},
		}}

		reached := false
		err := l.dispatch("p", func(client any) error {
			reached = true
			if client != "client-handle" {
				t.Errorf("state %s: dispatch passed the wrong client %v", st, client)
			}
			return nil
		})

		wantServe := st == StateReady
		if reached != wantServe {
			t.Errorf("state %s: process reached=%v, want %v", st, reached, wantServe)
		}
		if wantServe {
			if err != nil {
				t.Errorf("state %s (ready): dispatch returned %v, want nil", st, err)
			}
		} else if !errors.Is(err, ErrNotReady) {
			t.Errorf("state %s: dispatch returned %v, want ErrNotReady (and no process call)", st, err)
		}
	}
}

// An unknown plugin name is never-ready too — no panic, no call, a clear ErrNotReady.
func TestDispatchUnknownPluginIsNotReady(t *testing.T) {
	l := &Loader{plugins: map[string]*managed{}}
	reached := false
	err := l.dispatch("nope", func(any) error { reached = true; return nil })
	if reached {
		t.Fatal("dispatch reached a process for an unknown plugin")
	}
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("unknown plugin: got %v, want ErrNotReady", err)
	}
}
