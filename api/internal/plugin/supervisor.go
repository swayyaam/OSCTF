package plugin

import (
	"context"
	"log/slog"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/osctf/platform/internal/clock"
)

// superConfig holds the restart policy and its injected time sources. Zero fields are filled
// with the spec defaults by newSupervisor; tests override maxAttempts/healthStable and inject
// an instant sleeper + a fake clock so the ~6s of real backoff and the 60s stability window
// cost no wall-clock time.
type superConfig struct {
	maxAttempts  int           // total launch attempts before quarantine (spec: 5)
	baseBackoff  time.Duration // full-jitter exponential base (spec: 200ms)
	maxBackoff   time.Duration // backoff cap (spec: 30s)
	healthStable time.Duration // continuous-ready time that forgives prior failures (spec: 60s)
	sleep        sleeper       // backoff wait (production realSleep; tests instantSleep)
	now          clock.Clock   // time source for the stability window (injected like the scheduler)
	log          *slog.Logger
}

// supervisor drives ONE plugin through its lifecycle. It is the single writer of the plugin's
// state and client (invariant: exactly one writer per plugin) — the run goroutine owns
// attempts/readySince/conn outright and mutates the shared `managed` only through the loader
// lock, which dispatch reads under. The restart policy lives entirely here: launch → on
// failure count an attempt → quarantine at the cap → forgive the counter only after sustained
// health.
type supervisor struct {
	l      *Loader
	name   string
	launch launchFn
	cfg    superConfig

	launches atomic.Int32  // observable attempt count (tests/metrics); actor increments before each launch
	done     chan struct{} // closed when the run goroutine has fully torn down

	// actor-owned — touched ONLY by run and its helpers, so they need no lock:
	attempts   int       // consecutive failed attempts not yet forgiven by sustained health
	readySince time.Time // when the current ready streak began (zero if not ready)
}

func newSupervisor(l *Loader, name string, launch launchFn, cfg superConfig) *supervisor {
	if cfg.now == nil {
		cfg.now = clock.System()
	}
	if cfg.sleep == nil {
		cfg.sleep = realSleep
	}
	if cfg.log == nil {
		cfg.log = slog.Default()
	}
	if cfg.maxAttempts <= 0 {
		cfg.maxAttempts = 5
	}
	if cfg.baseBackoff <= 0 {
		cfg.baseBackoff = 200 * time.Millisecond
	}
	if cfg.maxBackoff <= 0 {
		cfg.maxBackoff = 30 * time.Second
	}
	if cfg.healthStable <= 0 {
		cfg.healthStable = 60 * time.Second
	}
	return &supervisor{l: l, name: name, launch: launch, cfg: cfg, done: make(chan struct{})}
}

// start launches the actor goroutine. It returns immediately; observe progress via state().
func (s *supervisor) start(ctx context.Context) { go s.run(ctx) }

// run is the actor loop: one iteration per launch attempt. It blocks synchronously in launch
// and backoff (both bounded and ctx-cancellable, so a stop returns promptly) and, once ready,
// blocks in a select on the process-exit watcher and ctx. It is the ONLY goroutine that writes
// this plugin's state.
func (s *supervisor) run(ctx context.Context) {
	defer close(s.done)

	for {
		// ---- LAUNCH ----
		s.transition(StateLaunching)
		s.launches.Add(1)
		c, err := s.launch(ctx)
		if ctx.Err() != nil { // stopped mid-launch
			if c != nil {
				c.kill()
			}
			return
		}
		if err != nil {
			s.cfg.log.Warn("plugin launch failed", "plugin", s.name, "attempt", s.attempts+1, "err", err)
			if s.backoffOrQuarantine(ctx, s.attempts+1 >= s.cfg.maxAttempts) {
				return // quarantined (parked in failed) or stopped
			}
			continue // relaunch
		}

		// ---- READY ----
		s.becomeReady(c)

		exited := make(chan struct{})
		wctx, wcancel := context.WithCancel(ctx)
		go func() { c.wait(wctx); close(exited) }()

		select {
		case <-ctx.Done(): // clean stop of a running plugin
			wcancel()
			s.teardown(c)
			return
		case <-exited: // the process died on its own — a crash
			wcancel()
			c.kill() // reap the dead process's client-side resources (idempotent; process already gone)
			s.markUnhealthy()
			// A crash after sustained health forgives the prior loop; a crash within the
			// stability window continues it. Reaching ready is NOT enough — otherwise a plugin
			// that dies every few seconds would reset every cycle and never quarantine.
			if s.cfg.now().Sub(s.readySince) >= s.cfg.healthStable {
				s.attempts = 0
			}
			s.readySince = time.Time{}
			s.cfg.log.Warn("plugin crashed", "plugin", s.name, "attempt", s.attempts+1)
			if s.backoffOrQuarantine(ctx, s.attempts+1 >= s.cfg.maxAttempts) {
				return
			}
			continue
		}
	}
}

// backoffOrQuarantine records one failed attempt and either quarantines (when the cap is hit)
// or waits the backoff before the caller relaunches. It returns true when the actor should
// STOP looping — quarantined into `failed` (parked until stop/reload) or the context ended —
// and false when it should launch again. `atCap` says whether this attempt reaches the cap.
func (s *supervisor) backoffOrQuarantine(ctx context.Context, atCap bool) (stop bool) {
	s.attempts++
	if atCap {
		s.quarantine()
		s.cfg.log.Error("plugin quarantined after crash loop", "plugin", s.name, "attempts", s.attempts)
		<-ctx.Done() // park in `failed`; only an explicit reload or stop leaves quarantine (#5/#7)
		return true
	}
	s.transition(StateRestarting)
	d := backoffDelay(s.cfg.baseBackoff, s.cfg.maxBackoff, s.attempts)
	if err := s.cfg.sleep(ctx, d); err != nil {
		return true // ctx cancelled during backoff
	}
	return false
}

// backoffDelay is full-jitter exponential backoff: base·2^(attempt-1), capped, then a uniform
// random point in [0, that]. Full jitter spreads a fleet of simultaneously-crashing plugins so
// they do not relaunch in lockstep.
func backoffDelay(base, max time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	if d <= 0 {
		return 0
	}
	//nolint:gosec // G404: jitter spreads restarts; it is not security-sensitive.
	return time.Duration(rand.Int63n(int64(d) + 1))
}

// --- state writes: the actor mutates the shared entry only through the loader lock ---

func (s *supervisor) transition(next State) {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	if err := s.l.plugins[s.name].m.to(next); err != nil {
		// An illegal transition is a supervisor bug, not a plugin fault; surface it loudly.
		s.cfg.log.Error("illegal plugin state transition", "plugin", s.name, "err", err)
	}
}

// becomeReady publishes the client and flips to ready in one locked step, then records the
// start of the ready streak (actor-owned, so outside the lock).
func (s *supervisor) becomeReady(c *conn) {
	s.l.mu.Lock()
	if err := s.l.plugins[s.name].m.to(StateReady); err != nil {
		s.cfg.log.Error("illegal plugin state transition", "plugin", s.name, "err", err)
	}
	s.l.plugins[s.name].client = c.client
	s.l.mu.Unlock()
	s.readySince = s.cfg.now()
}

// markUnhealthy leaves ready and withdraws the client in ONE locked step, so dispatch never
// observes ready-with-a-dead-client: the instant the state stops being ready, routing stops.
// The only remaining crash-detection gap is the watcher's poll latency, during which the
// client is still published but a call to it returns a mapped UNAVAILABLE, never a hang.
func (s *supervisor) markUnhealthy() {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	if err := s.l.plugins[s.name].m.to(StateUnhealthy); err != nil {
		s.cfg.log.Error("illegal plugin state transition", "plugin", s.name, "err", err)
	}
	s.l.plugins[s.name].client = nil
}

// quarantine moves an unhealthy/launching plugin to terminal `failed` via the legal path and
// withdraws any client. From launching the transition is direct; from unhealthy it routes
// through restarting (both legal).
func (s *supervisor) quarantine() {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	mg := s.l.plugins[s.name]
	if mg.m.state == StateUnhealthy {
		_ = mg.m.to(StateRestarting)
	}
	if err := mg.m.to(StateFailed); err != nil {
		s.cfg.log.Error("illegal plugin state transition", "plugin", s.name, "err", err)
	}
	mg.client = nil
}

// teardown reaps a running plugin on a clean stop: ready → draining → stopped, killing the
// process and withdrawing the client. (#7 makes the resource accounting rigorous; this is the
// state half.)
func (s *supervisor) teardown(c *conn) {
	s.transition(StateDraining)
	c.kill()
	s.l.mu.Lock()
	if err := s.l.plugins[s.name].m.to(StateStopped); err != nil {
		s.cfg.log.Error("illegal plugin state transition", "plugin", s.name, "err", err)
	}
	s.l.plugins[s.name].client = nil
	s.l.mu.Unlock()
}

// state reads the plugin's current state under the loader lock.
func (s *supervisor) state() State {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	return s.l.plugins[s.name].m.state
}
