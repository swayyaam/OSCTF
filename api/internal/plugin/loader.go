package plugin

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/metrics"
)

// ErrNotReady is returned by dispatch when a plugin is not in `ready` state (including
// absent). The type-specific layer above turns it into the per-type answer from the spec:
// a clear error for auth/challenge-type, an opt-in fallback for scoring, a counted drop
// for notification — but the plugin PROCESS is never called from a non-ready state.
var ErrNotReady = errors.New("plugin: not ready")

// ShedLevel says which in-flight cap shed a call — the distinction an operator needs, since
// "one plugin at its own cap" and "all plugins exhausting the shared budget" call for different
// responses (throttle/replace that plugin vs. raise the global budget / add capacity).
type ShedLevel int

const (
	ShedPerPlugin ShedLevel = iota // one plugin hit OSCTF_PLUGIN_MAX_INFLIGHT
	ShedGlobal                     // the shared budget (all plugins) is exhausted
)

func (l ShedLevel) label() string {
	if l == ShedGlobal {
		return "global"
	}
	return "per_plugin"
}

// ShedError is returned when a call is shed at an in-flight cap after waiting the queue budget.
// It maps to 503 (errors.Is(err, apperr.ErrUnavailable)) and its message names WHICH cap fired,
// so the operator log distinguishes the two causes.
type ShedError struct {
	Plugin     string
	Level      ShedLevel
	RetryAfter time.Duration
}

func (e *ShedError) Error() string {
	if e.Level == ShedGlobal {
		return "plugin " + e.Plugin + ": global plugin budget exhausted"
	}
	return "plugin " + e.Plugin + ": at per-plugin in-flight cap"
}

// Is lets the existing 503 mapping treat a shed as Unavailable without importing this package.
func (e *ShedError) Is(target error) bool { return target == apperr.ErrUnavailable }

// budget is the two-level in-flight limit: a per-plugin cap (size of each plugin's own
// semaphore, created in track) and a global cap shared across all plugins (one semaphore on the
// loader). A call acquires its per-plugin slot then the global slot, each waiting at most
// queueWait (also bounded by the caller's context), then sheds. The global sem is nil when
// unbounded.
type budget struct {
	perPluginCap int
	global       chan struct{}
	queueWait    time.Duration
}

// newBudget builds a budget. globalCap <= 0 means an unbounded global (per-plugin caps still
// apply). Used by the wiring (from the fd accountant) and by tests that force shedding.
func newBudget(perPluginCap, globalCap int, queueWait time.Duration) budget {
	b := budget{perPluginCap: perPluginCap, queueWait: queueWait}
	if globalCap > 0 {
		b.global = make(chan struct{}, globalCap)
	}
	return b
}

// managed is one plugin the loader tracks: identity, state, the live connection to its current
// process (nil until ready), and its per-plugin in-flight semaphore.
type managed struct {
	name string
	m    machine
	cur  *conn         // the current instance's connection; nil unless ready
	sem  chan struct{} // per-plugin in-flight semaphore (cap = budget.perPluginCap)
}

// Loader discovers, launches, supervises, and routes to plugins. It holds the tracked set under
// a mutex, dispatches calls only to `ready` plugins, and bounds concurrent host→plugin work with
// the two-level budget.
type Loader struct {
	mu      sync.Mutex
	plugins map[string]*managed
	budget  budget
}

// newLoader returns an empty loader with a permissive default budget (per-plugin 64, unbounded
// global, 1s queue wait). The wiring replaces the budget from the fd accountant; tests that
// exercise shedding set a small one before tracking.
func newLoader() *Loader {
	return &Loader{plugins: map[string]*managed{}, budget: newBudget(64, 0, time.Second)}
}

// track records a newly discovered plugin in `discovered` state, sizing its per-plugin
// semaphore from the current budget, and returns its entry.
func (l *Loader) track(name string) *managed {
	l.mu.Lock()
	defer l.mu.Unlock()
	cap := l.budget.perPluginCap
	if cap <= 0 {
		cap = 64
	}
	mg := &managed{name: name, m: machine{state: StateDiscovered}, sem: make(chan struct{}, cap)}
	l.plugins[name] = mg
	return mg
}

// dispatch runs call against the plugin's client ONLY if the plugin is `ready`, and only after
// acquiring an in-flight slot (per-plugin then global) — otherwise ErrNotReady, or a ShedError
// if the budget is exhausted past the queue wait. It records the call's latency and keeps the
// per-plugin in-flight gauge and the connection's drain counter, both released when the call
// returns however it ends (success, error, or the caller's context being cancelled) — so a slot
// is never leaked, including on the drain-timeout path.
func (l *Loader) dispatch(ctx context.Context, name, method string, call func(client any) error) error {
	l.mu.Lock()
	p, ok := l.plugins[name]
	serve := ok && p.m.servesNew() && p.cur != nil
	var c *conn
	var sem chan struct{}
	if serve {
		c = p.cur
		sem = p.sem
	}
	l.mu.Unlock()

	if !serve {
		return ErrNotReady
	}

	release, err := l.acquire(ctx, name, sem)
	if err != nil {
		return err
	}
	defer release()

	c.inflight.Add(1)
	metrics.PluginInflight.WithLabelValues(name).Inc()
	defer func() {
		c.inflight.Add(-1)
		metrics.PluginInflight.WithLabelValues(name).Dec()
	}()

	start := time.Now()
	callErr := call(c.client)
	metrics.PluginCallDuration.WithLabelValues(name, method).Observe(time.Since(start).Seconds())
	return callErr
}

// acquire takes a per-plugin slot then a global slot, each waiting at most queueWait (bounded by
// the caller's context too), releasing the per-plugin slot if the global one sheds. It never
// blocks indefinitely; a shed is counted and returned as a ShedError naming the cap.
func (l *Loader) acquire(ctx context.Context, name string, sem chan struct{}) (release func(), err error) {
	acqCtx, cancel := context.WithTimeout(ctx, l.budget.queueWait)
	defer cancel()

	if sem != nil {
		select {
		case sem <- struct{}{}:
		case <-acqCtx.Done():
			if ctx.Err() != nil {
				return nil, ctx.Err() // the caller gave up / its deadline fired
			}
			metrics.PluginInflightShed.WithLabelValues(name, ShedPerPlugin.label()).Inc()
			return nil, &ShedError{Plugin: name, Level: ShedPerPlugin, RetryAfter: l.budget.queueWait}
		}
	}

	if l.budget.global != nil {
		select {
		case l.budget.global <- struct{}{}:
		case <-acqCtx.Done():
			if sem != nil {
				<-sem // release the per-plugin slot we already took
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			metrics.PluginInflightShed.WithLabelValues(name, ShedGlobal.label()).Inc()
			return nil, &ShedError{Plugin: name, Level: ShedGlobal, RetryAfter: l.budget.queueWait}
		}
	}

	return func() {
		if l.budget.global != nil {
			<-l.budget.global
		}
		if sem != nil {
			<-sem
		}
	}, nil
}
