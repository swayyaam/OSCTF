package challenges

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// ChallengeType decides a challenge's author-time config validation and (in P4) its custom
// flag check. In P1 only the built-in types exist and ValidateConfig is a no-op — the core
// validation (validateCreate/validateUpdate) already covers standard/container; CheckFlag
// does not enter the submission transaction until a plugin needs it (see docs/v0.3).
type ChallengeType interface {
	ID() string
	// ValidateConfig checks type-specific config at authoring time, returning per-field
	// errors (empty = ok). The built-in types validate nothing extra here.
	ValidateConfig(cfg map[string]string) map[string][]string
}

type builtinType struct{ id string }

func (b builtinType) ID() string                                         { return b.id }
func (builtinType) ValidateConfig(map[string]string) map[string][]string { return nil }

type ctEntry struct {
	ct        ChallengeType
	protected bool
}

// TypeRegistry resolves a challenge-type id to its ChallengeType. Same atomic-swap model as
// the auth and scoring registries: an atomic pointer to an IMMUTABLE map — readers
// (Get/IsRegistered) are lock-free, Register copies-on-write and swaps under a writer mutex,
// so a swap is atomic from a reader's view. With only the built-ins registered it is
// behaviourally identical to v0.2.2 (every challenge is 'standard'/'container').
type TypeRegistry struct {
	m       atomic.Pointer[map[string]ctEntry]
	writeMu sync.Mutex
}

// NewTypeRegistry builds a registry with the given types registered as protected built-ins.
func NewTypeRegistry(builtins ...ChallengeType) *TypeRegistry {
	r := &TypeRegistry{}
	m := make(map[string]ctEntry, len(builtins))
	for _, ct := range builtins {
		m[ct.ID()] = ctEntry{ct: ct, protected: true}
	}
	r.m.Store(&m)
	return r
}

// Get resolves a type by id. Lock-free.
func (r *TypeRegistry) Get(id string) (ChallengeType, bool) {
	e, ok := (*r.m.Load())[id]
	if !ok {
		return nil, false
	}
	return e.ct, true
}

// IsRegistered reports whether a type id resolves. Lock-free.
func (r *TypeRegistry) IsRegistered(id string) bool {
	_, ok := (*r.m.Load())[id]
	return ok
}

// Register adds or replaces a type. A protected built-in is refused unless override is true.
// The map is swapped atomically.
func (r *TypeRegistry) Register(id string, ct ChallengeType, override bool) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := *r.m.Load()
	if e, exists := old[id]; exists && e.protected && !override {
		return fmt.Errorf("challenges: type %q is a protected built-in; pass override to replace it", id)
	}
	next := make(map[string]ctEntry, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	protected := false
	if e, exists := old[id]; exists {
		protected = e.protected
	}
	next[id] = ctEntry{ct: ct, protected: protected}
	r.m.Store(&next)
	return nil
}

// Names returns the registered type ids (for admin listing).
func (r *TypeRegistry) Names() []string {
	m := *r.m.Load()
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// defaultTypeRegistry backs the package-level convenience functions. The built-in
// standard/container types are registered as protected; the plugin loader calls
// RegisterType (P4).
var defaultTypeRegistry = NewTypeRegistry(builtinType{"standard"}, builtinType{"container"})

// RegisterType adds/replaces a challenge type in the default registry (the loader's entry).
func RegisterType(id string, ct ChallengeType, override bool) error {
	return defaultTypeRegistry.Register(id, ct, override)
}

// IsRegisteredType reports whether a challenge-type id is registered in the default registry.
func IsRegisteredType(id string) bool { return defaultTypeRegistry.IsRegistered(id) }

// TypeNames returns the registered type ids (default registry).
func TypeNames() []string { return defaultTypeRegistry.Names() }
