package plugin

import (
	"errors"
	"sync"
)

// ErrNotReady is returned by dispatch when a plugin is not in `ready` state (including
// absent). The type-specific layer above turns it into the per-type answer from the spec:
// a clear error for auth/challenge-type, an opt-in fallback for scoring, a counted drop
// for notification — but the plugin PROCESS is never called from a non-ready state.
var ErrNotReady = errors.New("plugin: not ready")

// managed is one plugin the loader tracks: its identity, current state, and — once
// launched and registered — the dispensed gRPC client handle to its process. The full
// launch/supervision fields are added with the lifecycle invariants; this is the routing
// core the state machine gates.
type managed struct {
	name   string
	m      machine
	client any // the dispensed type-specific client (ScoringClient, …); nil until ready
}

// Loader discovers, launches, supervises, and routes to plugins. This file is the routing
// core: it holds the tracked set under a mutex and dispatches calls only to `ready`
// plugins. Discovery, launch, health, restart, reload, and stop are layered on in the
// lifecycle steps (each pinned by its own invariant before it is written).
type Loader struct {
	mu      sync.Mutex
	plugins map[string]*managed
}

// dispatch runs call against the plugin's client ONLY if the plugin is `ready`; otherwise
// it returns ErrNotReady without touching the client — invariant #2. The readiness check
// and the client read happen under the lock so a concurrent state change can't let a call
// slip through to a plugin that just left `ready`.
func (l *Loader) dispatch(name string, call func(client any) error) error {
	l.mu.Lock()
	p, ok := l.plugins[name]
	serve := ok && p.m.servesNew()
	var client any
	if serve {
		client = p.client
	}
	l.mu.Unlock()

	if !serve {
		return ErrNotReady
	}
	return call(client)
}
