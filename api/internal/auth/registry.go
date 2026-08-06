package auth

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Registry resolves a named AuthProvider. v0.3 replaces the single injected provider
// with a registry so the plugin loader can add providers (OIDC, GitHub, …) alongside
// the built-in email/password one; with only the built-in registered it is
// behaviourally identical to the injected provider.
//
// Concurrency (v0.3 decision): the provider map is IMMUTABLE and held behind an atomic
// pointer. Readers (Get/Default, on the login hot path) load the pointer lock-free and
// read a map that never changes under them. A writer (Register, at boot and later on a
// plugin reload) builds a fresh map copy-on-write and atomically swaps the pointer, under
// a mutex that serialises writers only. So a register/reload is atomic from a reader's
// perspective: a lookup in flight resolves to the old map or the new one, never a torn or
// partially-updated map, never nothing. Pinned by TestAuthRegistryReaderAtomicSwap.
type Registry struct {
	m           atomic.Pointer[map[string]entry]
	defaultName string
	writeMu     sync.Mutex // serialises Register; readers never take it
}

type entry struct {
	provider  AuthProvider
	protected bool // a built-in: not replaceable without an explicit override
}

// NewRegistry builds a registry whose default (the credential provider for the primary
// email/password login) is def, registered as a protected built-in.
func NewRegistry(def AuthProvider) *Registry {
	r := &Registry{defaultName: def.Name()}
	m := map[string]entry{def.Name(): {provider: def, protected: true}}
	r.m.Store(&m)
	return r
}

// Get resolves a provider by name. Lock-free.
func (r *Registry) Get(name string) (AuthProvider, bool) {
	e, ok := (*r.m.Load())[name]
	if !ok {
		return nil, false
	}
	return e.provider, true
}

// Default returns the primary credential provider (the built-in registered at
// construction) — what POST /auth/login authenticates against.
func (r *Registry) Default() AuthProvider {
	return (*r.m.Load())[r.defaultName].provider
}

// Register adds or replaces a provider. A protected built-in is refused unless override
// is true. Plugin-registered providers are not protected. The map is swapped atomically.
func (r *Registry) Register(name string, p AuthProvider, override bool) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()

	old := *r.m.Load()
	if e, exists := old[name]; exists && e.protected && !override {
		return fmt.Errorf("auth: provider %q is a protected built-in; pass override to replace it", name)
	}
	next := make(map[string]entry, len(old)+1)
	for k, v := range old {
		next[k] = v
	}
	// An explicit override of a built-in keeps it protected; a fresh registration is not.
	protected := false
	if e, exists := old[name]; exists {
		protected = e.protected
	}
	next[name] = entry{provider: p, protected: protected}
	r.m.Store(&next)
	return nil
}

// Names returns the registered provider names (for admin listing / GET /auth/providers).
func (r *Registry) Names() []string {
	m := *r.m.Load()
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	return names
}
