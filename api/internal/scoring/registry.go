package scoring

import (
	"fmt"
	"sync"
	"sync/atomic"
)

type engEntry struct {
	engine    ScoringEngine
	protected bool
}

// Registry resolves a scoring mode name to its engine. v0.3 makes the previously-static
// map mutable + override-protected so the plugin loader can add scoring modes; with only
// the built-ins registered it is behaviourally identical to v0.2.2.
//
// Concurrency (v0.3 decision): an atomic pointer to an IMMUTABLE map. Readers (Get/Value,
// the scoreboard hot path) are lock-free; Register copies-on-write and swaps the pointer
// under a writer mutex, so a swap is atomic from a reader's view — a lookup in flight
// resolves to the old map or the new one, never a torn or missing engine. Pinned by
// TestScoringRegistryReaderAtomicContention.
type Registry struct {
	m       atomic.Pointer[map[string]engEntry]
	writeMu sync.Mutex
}

// NewRegistry builds a registry with the given engines registered as protected built-ins.
func NewRegistry(builtins ...ScoringEngine) *Registry {
	r := &Registry{}
	m := make(map[string]engEntry, len(builtins))
	for _, e := range builtins {
		m[e.Name()] = engEntry{engine: e, protected: true}
	}
	r.m.Store(&m)
	return r
}

// Get resolves an engine by mode name. Lock-free.
func (r *Registry) Get(mode string) (ScoringEngine, bool) {
	e, ok := (*r.m.Load())[mode]
	if !ok {
		return nil, false
	}
	return e.engine, true
}

// Value selects the engine by mode and computes the value; an unknown mode falls back to
// static (v0.2.2 behaviour).
func (r *Registry) Value(mode string, params ChallengeScoring, solves int) int {
	if e, ok := (*r.m.Load())[mode]; ok {
		return e.engine.Value(params, solves)
	}
	return StaticEngine{}.Value(params, solves)
}

// Register adds or replaces an engine. A protected built-in is refused unless override is
// true; plugin-registered engines are not protected. The map is swapped atomically.
func (r *Registry) Register(name string, e ScoringEngine, override bool) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := *r.m.Load()
	if ex, exists := old[name]; exists && ex.protected && !override {
		return fmt.Errorf("scoring: mode %q is a protected built-in; pass override to replace it", name)
	}
	next := make(map[string]engEntry, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	protected := false
	if ex, exists := old[name]; exists {
		protected = ex.protected
	}
	next[name] = engEntry{engine: e, protected: protected}
	r.m.Store(&next)
	return nil
}

// Engines returns a snapshot copy of the current mode→engine map (for listing).
func (r *Registry) Engines() map[string]ScoringEngine {
	old := *r.m.Load()
	out := make(map[string]ScoringEngine, len(old))
	for k, v := range old {
		out[k] = v.engine
	}
	return out
}

// defaultRegistry backs the package-level convenience functions. Existing callers
// (scoreboard/compute, challenges, submissions) call scoring.Value unchanged; the plugin
// loader calls scoring.Register.
var defaultRegistry = NewRegistry(StaticEngine{}, DynamicEngine{})

// Value selects the engine by mode and computes the value against the default registry.
// Unknown modes fall back to static. Signature unchanged from v0.2.2.
func Value(mode string, params ChallengeScoring, solves int) int {
	return defaultRegistry.Value(mode, params, solves)
}

// Register adds/replaces an engine in the default registry — the plugin loader's entry.
func Register(name string, e ScoringEngine, override bool) error {
	return defaultRegistry.Register(name, e, override)
}

// Engines returns a snapshot of the default registry's engines (replaces the former
// Registry() function).
func Engines() map[string]ScoringEngine {
	return defaultRegistry.Engines()
}
