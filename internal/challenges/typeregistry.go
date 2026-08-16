package challenges

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// ConfigValidation is the result of author-time challenge-type config validation — the host mirror
// of the wire ValidateResponse and the SDK's sdk.ConfigValidation, so all three layers speak ONE
// reconciled shape. OK reports whether the config is usable; FieldErrors carries ONE message per
// rejected field (rendered into the 422 errors map an admin sees); Normalized is the canonicalised
// config the host stores in place of the raw author input. When Normalized is empty the host stores
// the author's input unchanged.
type ConfigValidation struct {
	OK          bool
	FieldErrors map[string]string
	Normalized  map[string]string
}

// ChallengeType decides a challenge's author-time config validation and (via the optional
// FlagChecker below) its custom flag check. The built-in types (standard/container) implement
// only this interface and defer correctness to the platform's flag comparison; a plugin type
// additionally implements FlagChecker and owns correctness.
type ChallengeType interface {
	ID() string
	// ValidateConfig checks a challenge's per-challenge type config at authoring time. OK=false
	// with per-field messages rejects the write (422); a non-nil error means the type could not be
	// reached to validate (its plugin is down) and the write fails CLOSED, rather than storing
	// unvalidated config. The built-in types reach nothing and always accept. ctx bounds the dial.
	ValidateConfig(ctx context.Context, cfg map[string]string) (ConfigValidation, error)
}

// FlagChecker is implemented ONLY by a plugin-provided challenge-type: it decides correctness
// from the submitted guess + author config + per-instance metadata (never the real flag). The
// submission path type-asserts for it — a type that implements it takes the PRE-transaction
// plugin verdict path; a built-in (which does not) keeps the platform's in-transaction flag
// comparison, byte-identical to v0.2.
type FlagChecker interface {
	CheckFlag(ctx context.Context, submitted string, config, instance map[string]string) (correct bool, err error)
}

type builtinType struct{ id string }

func (b builtinType) ID() string { return b.id }
func (builtinType) ValidateConfig(context.Context, map[string]string) (ConfigValidation, error) {
	return ConfigValidation{OK: true}, nil
}

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
	base    map[string]ctEntry // immutable built-ins, restored when a plugin override is reverted
	writeMu sync.Mutex
}

// NewTypeRegistry builds a registry with the given types registered as protected built-ins.
func NewTypeRegistry(builtins ...ChallengeType) *TypeRegistry {
	r := &TypeRegistry{}
	m := make(map[string]ctEntry, len(builtins))
	for _, ct := range builtins {
		m[ct.ID()] = ctEntry{ct: ct, protected: true}
	}
	r.base = make(map[string]ctEntry, len(m))
	for k, v := range m {
		r.base[k] = v
	}
	r.m.Store(&m)
	return r
}

// Deregister removes a plugin-registered type (revert-before-death when its plugin stops),
// restoring the built-in a plugin had overridden, or removing the entry outright otherwise. A
// bare built-in cannot "stop", so deregistering one is a no-op. Atomic swap (reader-safe). After
// this, the submission path finds the type unregistered and fails a submission to it closed.
func (r *TypeRegistry) Deregister(id string) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	old := *r.m.Load()
	if _, exists := old[id]; !exists {
		return
	}
	next := make(map[string]ctEntry, len(old))
	for k, v := range old {
		if k == id {
			continue
		}
		next[k] = v
	}
	if b, isBuiltin := r.base[id]; isBuiltin {
		next[id] = b
	}
	r.m.Store(&next)
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

// DefaultTypeRegistry returns the process-wide challenge-type registry — the one CRUD validation
// checks, plugins register into, and the submission path resolves against, so all three agree on
// which types exist.
func DefaultTypeRegistry() *TypeRegistry { return defaultTypeRegistry }

// TypeNames returns the registered type ids (default registry).
func TypeNames() []string { return defaultTypeRegistry.Names() }
