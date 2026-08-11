package plugin

import (
	"context"
	"log/slog"
)

// Caller invokes a method on ONE plugin with the loader's readiness gate and in-flight budget
// applied — the handle a registered provider (a scoring engine, auth provider, challenge-type
// checker) uses to reach the plugin process. A call to a non-ready plugin returns ErrNotReady
// without touching the process, which is the fail-closed layer INSIDE every registered provider.
type Caller interface {
	Call(ctx context.Context, method string, fn func(ctx context.Context, client any) error) error
}

// Registrar wires a ready plugin's provider into its type's registry and reverts it before the
// plugin dies. The composition root implements it per type (auth/scoring/challenge-type/
// notification); the loader calls Register once when a plugin FIRST becomes ready and Deregister
// once when it terminates — before the process is killed (revert-before-death).
type Registrar interface {
	Register(name, ptype string, caller Caller) error
	Deregister(name, ptype string)
}

// noopRegistrar is used when no registrar is wired (the lifecycle tests, and pure transport
// paths): the loader still supervises, it just does not publish providers.
type noopRegistrar struct{}

func (noopRegistrar) Register(string, string, Caller) error { return nil }
func (noopRegistrar) Deregister(string, string)             {}

// boundCaller binds the loader's dispatch to one plugin name.
type boundCaller struct {
	l    *Loader
	name string
}

func (c boundCaller) Call(ctx context.Context, method string, fn func(ctx context.Context, client any) error) error {
	return c.l.dispatch(ctx, c.name, method, fn)
}

func (l *Loader) callerFor(name string) Caller { return boundCaller{l: l, name: name} }

func (l *Loader) registrar() Registrar {
	if l.cfg.Registrar == nil {
		return noopRegistrar{}
	}
	return l.cfg.Registrar
}

func (l *Loader) log() *slog.Logger {
	if l.cfg.Log != nil {
		return l.cfg.Log
	}
	return slog.Default()
}

// setType records a plugin's manifest type before its supervisor starts, so registerReady knows
// which registry to publish into.
func (l *Loader) setType(name, ptype string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if mg, ok := l.plugins[name]; ok {
		mg.ptype = ptype
	}
}

// registerReady publishes a plugin's provider into its type registry the FIRST time it becomes
// ready. It is register-ONCE: the provider (a boundCaller-backed adapter) is process-agnostic and
// survives restarts and reloads, and the dispatch ready-gate inside it fails calls closed during
// a transient `unhealthy` — so a blip-and-recover never removes and re-adds the entry (no
// registry flap). The entry is removed only on a terminal stop/quarantine (see revert).
func (l *Loader) registerReady(name string) {
	l.mu.Lock()
	mg, ok := l.plugins[name]
	if !ok || mg.registered {
		l.mu.Unlock()
		return
	}
	mg.registered = true
	ptype := mg.ptype
	l.mu.Unlock()

	if err := l.registrar().Register(name, ptype, l.callerFor(name)); err != nil {
		l.log().Error("plugin provider registration failed; it will not serve via its registry", "plugin", name, "type", ptype, "err", err)
		l.mu.Lock()
		if mg, ok := l.plugins[name]; ok {
			mg.registered = false
		}
		l.mu.Unlock()
	}
}

// revert removes a plugin's provider from its type registry — called before the process is killed
// on a terminal stop or quarantine (revert-before-death), so a lookup after that resolves to the
// built-in or nothing, never to a handle whose process is gone. Idempotent.
func (l *Loader) revert(name string) {
	l.mu.Lock()
	mg, ok := l.plugins[name]
	if !ok || !mg.registered {
		l.mu.Unlock()
		return
	}
	mg.registered = false
	ptype := mg.ptype
	l.mu.Unlock()

	l.registrar().Deregister(name, ptype)
}
