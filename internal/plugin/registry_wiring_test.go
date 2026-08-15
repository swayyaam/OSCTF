package plugin

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/scoring"
)

// recorder is an ordered, concurrency-safe event log shared by the fake registrar and the fake
// conn's kill, so a test can assert their relative order (revert-before-kill).
type recorder struct {
	mu     sync.Mutex
	events []string
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *recorder) count(e string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, x := range r.events {
		if x == e {
			n++
		}
	}
	return n
}

type recordingRegistrar struct{ rec *recorder }

func (rr recordingRegistrar) Register(string, string, Caller) error {
	rr.rec.add("register")
	return nil
}
func (rr recordingRegistrar) Deregister(string, string) { rr.rec.add("deregister") }

// indexOf returns the position of the first occurrence of e, or -1.
func indexOf(s []string, e string) int {
	for i, x := range s {
		if x == e {
			return i
		}
	}
	return -1
}

// Invariant #3 (revert-before-death): on a clean stop, the provider is deregistered from its
// registry BEFORE the process is killed — so a lookup after that resolves to the built-in or
// nothing, never a handle whose process is gone. Proven by ordering: register (on ready) →
// deregister (revert) → kill, in that order.
func TestRegistryRevertBeforeKill(t *testing.T) {
	rec := &recorder{}
	l := newLoader()
	l.cfg.Registrar = recordingRegistrar{rec}
	l.track("p")
	l.setType("p", "scoring")

	launch := func(context.Context) (*conn, error) {
		return &conn{
			client: "fake",
			kill:   func() { rec.add("kill") },
			wait:   func(wctx context.Context) { <-wctx.Done() }, // never crashes; exits on stop
		}, nil
	}
	s := newSupervisor(l, "p", launch, superConfig{sleep: instantSleep, now: clock.System()})

	ctx, cancel := context.WithCancel(context.Background())
	s.start(ctx)
	waitForState(t, s, StateReady, 5*time.Second)

	cancel() // stop → teardown → revert → drain → kill
	<-s.done

	ev := rec.snapshot()
	ri, di, ki := indexOf(ev, "register"), indexOf(ev, "deregister"), indexOf(ev, "kill")
	if ri < 0 || di < 0 || ki < 0 {
		t.Fatalf("missing events in %v (register=%d deregister=%d kill=%d)", ev, ri, di, ki)
	}
	if ri >= di || di >= ki {
		t.Errorf("order = %v; want register < deregister < kill (revert must precede the kill)", ev)
	}
}

// Invariant #3 (point 2 — no flap): the provider is registered ONCE, on first ready, and survives
// restarts — a health blip that crashes and recovers does NOT deregister and re-register (which
// would flap logins/scoring). The dispatch ready-gate inside the provider is the fail-closed layer
// during the blip. Drives three ready cycles (crash between each) and asserts exactly one Register.
func TestRegistryRegisterOnceAcrossRestarts(t *testing.T) {
	rec := &recorder{}
	l := newLoader()
	l.cfg.Registrar = recordingRegistrar{rec}
	l.track("p")
	l.setType("p", "scoring")

	launched := make(chan chan struct{}, 16)
	launch := func(context.Context) (*conn, error) {
		cr := make(chan struct{})
		launched <- cr
		return &conn{
			client: "fake",
			kill:   func() {},
			wait: func(wctx context.Context) {
				select {
				case <-cr:
				case <-wctx.Done():
				}
			},
		}, nil
	}
	// maxAttempts high so three crashes don't quarantine; instant backoff.
	s := newSupervisor(l, "p", launch, superConfig{
		maxAttempts: 10, baseBackoff: time.Millisecond, sleep: instantSleep, now: clock.System(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-s.done }()
	s.start(ctx)

	for i := 0; i < 3; i++ {
		select {
		case cr := <-launched:
			waitForState(t, s, StateReady, 5*time.Second)
			close(cr) // crash → restart → ready again
		case <-time.After(5 * time.Second):
			t.Fatalf("cycle %d: no launch", i)
		}
	}
	// Let the third ready settle.
	waitForState(t, s, StateReady, 5*time.Second)

	if got := rec.count("register"); got != 1 {
		t.Errorf("Register called %d times across 3 restarts; want exactly 1 (no flap)", got)
	}
	if got := rec.count("deregister"); got != 0 {
		t.Errorf("Deregister called %d times during transient crashes; want 0 (revert only on terminal)", got)
	}
}

// Invariant #3 (the restarting window — a direct consequence of register-once): because the entry
// now survives `restarting`, the dispatch ready-gate is the SOLE thing between a caller and a
// plugin mid-relaunch. A call arriving during `restarting` must return ErrNotReady cleanly — no
// panic, and no call against the stale client handle of the process being replaced. Asserted
// directly by holding the supervisor in `restarting` (a blocked backoff) and dispatching.
func TestDispatchDuringRestartingIsNotReady(t *testing.T) {
	rec := &recorder{}
	l := newLoader()
	l.cfg.Registrar = recordingRegistrar{rec}
	l.track("p")
	l.setType("p", "scoring")

	launched := make(chan chan struct{}, 8)
	launch := func(context.Context) (*conn, error) {
		cr := make(chan struct{})
		launched <- cr
		return &conn{
			client: "stale-handle", // if the gate ever calls through, the fn sees this
			kill:   func() {},
			wait: func(wctx context.Context) {
				select {
				case <-cr:
				case <-wctx.Done():
				}
			},
		}, nil
	}
	block := make(chan struct{})
	// The backoff between the crash and the relaunch blocks here, holding the supervisor in
	// `restarting`; it still honours ctx so cleanup (cancel) is prompt.
	sleep := func(ctx context.Context, _ time.Duration) error {
		select {
		case <-block:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s := newSupervisor(l, "p", launch, superConfig{maxAttempts: 10, sleep: sleep, now: clock.System()})

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-s.done }()
	s.start(ctx)

	cr := <-launched
	waitForState(t, s, StateReady, 5*time.Second)
	close(cr) // crash → unhealthy → restarting (backoff blocks here)
	waitForState(t, s, StateRestarting, 5*time.Second)

	// Entry still present (register-once, no deregister on a transient crash) — so ONLY the gate
	// stands between the caller and the mid-relaunch plugin.
	if rec.count("register") != 1 || rec.count("deregister") != 0 {
		t.Fatalf("precondition: entry must still be registered during restarting (reg=%d dereg=%d)",
			rec.count("register"), rec.count("deregister"))
	}
	called := false
	err := l.dispatch(context.Background(), "p", "Value", func(context.Context, any) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrNotReady) {
		t.Errorf("dispatch during restarting = %v; want ErrNotReady", err)
	}
	if called {
		t.Error("gate let a call through to the stale client handle during restarting")
	}
}

// pluginScoringEngine is a scoring engine backed by a live plugin, used to wire a REAL
// scoring.Registry in the #3 tests. Its Value routes through the loader's Caller (readiness gate +
// budget) and falls back to static on any error. (#9 replaces this with the record-based,
// off-read-path design; here it exists only to exercise the register/revert/gate mechanism against
// a real atomic-pointer registry.)
type pluginScoringEngine struct {
	name string
	c    Caller
}

func (e pluginScoringEngine) Name() string { return e.name }
func (e pluginScoringEngine) Value(p scoring.ChallengeScoring, solves int) int {
	err := e.c.Call(context.Background(), "Value", func(context.Context, any) error { return nil })
	if err != nil {
		return scoring.StaticEngine{}.Value(p, solves) // fail closed to the built-in
	}
	return p.Initial
}

// scoringRegistrar wires ready scoring plugins into a real scoring.Registry — the concrete
// per-type Registrar shape the composition root uses.
type scoringRegistrar struct{ reg *scoring.Registry }

func (sr scoringRegistrar) Register(name, ptype string, c Caller) error {
	if ptype != "scoring" {
		return nil
	}
	return sr.reg.Register(name, pluginScoringEngine{name: name, c: c}, false)
}

func (sr scoringRegistrar) Deregister(name, ptype string) {
	if ptype == "scoring" {
		sr.reg.Deregister(name)
	}
}

// Invariant #3 (two layers, independently): the registry entry and the dispatch ready-gate must
// each fail closed on their own, so neither is silently doing all the work.
func TestRegistryAndGateAreIndependentLayers(t *testing.T) {
	reg := scoring.NewRegistry(scoring.StaticEngine{}, scoring.DynamicEngine{})
	l := newLoader()
	l.cfg.Registrar = scoringRegistrar{reg}
	l.track("elo")
	l.setType("elo", "scoring")

	launched := make(chan chan struct{}, 8)
	launch := func(context.Context) (*conn, error) {
		cr := make(chan struct{})
		launched <- cr
		return &conn{client: "x", kill: func() {}, wait: func(wctx context.Context) {
			select {
			case <-cr:
			case <-wctx.Done():
			}
		}}, nil
	}
	block := make(chan struct{}) // blocks the backoff so `restarting` is held open for the gate-alone check
	sleep := func(ctx context.Context, _ time.Duration) error {
		select {
		case <-block:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	s := newSupervisor(l, "elo", launch, superConfig{maxAttempts: 10, sleep: sleep, now: clock.System()})
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-s.done }()
	s.start(ctx)

	cr := <-launched
	waitForState(t, s, StateReady, 5*time.Second)

	// LAYER 1 — the registry entry exists while the plugin lives: Get resolves the plugin engine.
	if e, ok := reg.Get("elo"); !ok || e.Name() != "elo" {
		t.Fatalf("registry entry missing while plugin ready (ok=%v)", ok)
	}

	// GATE ALONE — leave the entry in place, make the plugin not-ready (crash → restarting, held by
	// the blocked backoff), and dispatch: the gate fails closed even though the registry still
	// holds the entry.
	close(cr)
	waitForState(t, s, StateRestarting, 5*time.Second)
	if _, ok := reg.Get("elo"); !ok {
		t.Error("entry was removed on a transient crash — gate layer cannot be tested in isolation")
	}
	if err := l.dispatch(context.Background(), "elo", "Value", func(context.Context, any) error { return nil }); !errors.Is(err, ErrNotReady) {
		t.Errorf("gate did not fail closed with the entry present: %v", err)
	}

	// REGISTRY ALONE — release the backoff to recover, then STOP (terminal): revert removes the
	// entry, so a lookup misses the plugin and resolves to the built-in, independent of the gate.
	close(block)
	<-launched // the relaunch
	waitForState(t, s, StateReady, 5*time.Second)
	cancel()
	<-s.done
	if e, ok := reg.Get("elo"); ok && e.Name() == "elo" {
		t.Error("registry still holds the plugin engine after stop — revert did not remove it")
	}
}

// Invariant #3 (adversarial, restart churn): readers hammering the registry while the plugin is
// killed and relaunched repeatedly — the state the entry now PERSISTS through — must always
// resolve to a valid provider (a live plugin or the built-in fallback), never panic, never a torn
// read, never a call against a stale client handle. Run under -race.
func TestAdversarialRegistryUnderRestartChurn(t *testing.T) {
	reg := scoring.NewRegistry(scoring.StaticEngine{}, scoring.DynamicEngine{})
	l := newLoader()
	l.cfg.Registrar = scoringRegistrar{reg}
	l.track("elo")
	l.setType("elo", "scoring")

	launched := make(chan chan struct{}, 64)
	launch := func(context.Context) (*conn, error) {
		cr := make(chan struct{})
		launched <- cr
		return &conn{client: "x", kill: func() {}, wait: func(wctx context.Context) {
			select {
			case <-cr:
			case <-wctx.Done():
			}
		}}, nil
	}
	s := newSupervisor(l, "elo", launch, superConfig{maxAttempts: 1000, baseBackoff: time.Millisecond, sleep: instantSleep, now: clock.System()})
	ctx, cancel := context.WithCancel(context.Background())
	s.start(ctx)

	stop := make(chan struct{})
	var readers sync.WaitGroup
	for i := 0; i < 8; i++ { // hammer the registry from many goroutines
		readers.Add(1)
		go func() {
			defer readers.Done()
			p := scoring.ChallengeScoring{Initial: 500, Min: 100, Decay: 50}
			for {
				select {
				case <-stop:
					return
				default:
				}
				if e, ok := reg.Get("elo"); ok {
					if v := e.Value(p, 3); v < 0 { // valid int always (live value or static fallback)
						t.Errorf("registry Value returned %d (< 0) — a torn/invalid resolution", v)
					}
				}
			}
		}()
	}

	// Churn restarts: repeatedly bring it ready then crash it, while readers hammer.
	for i := 0; i < 30; i++ {
		select {
		case cr := <-launched:
			waitForState(t, s, StateReady, 5*time.Second)
			close(cr) // crash → restart (entry persists)
		case <-time.After(5 * time.Second):
			t.Fatalf("cycle %d: no relaunch", i)
		}
	}

	close(stop)
	readers.Wait()
	cancel()
	<-s.done

	// After the final stop, the entry is reverted (never left pointing at a dead process).
	if e, ok := reg.Get("elo"); ok && e.Name() == "elo" {
		t.Error("registry still holds the plugin engine after stop")
	}
}
