package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"sync/atomic"
	"time"

	"github.com/osctf/platform/internal/clock"
)

// errStopped is returned by reload when the supervisor has already stopped.
var errStopped = errors.New("plugin: supervisor stopped")

// reloadReq is one hot-reload request; result carries the outcome back to the caller (nil once
// the new instance is ready and swapped in, an error if it never became ready and the old
// instance was retained).
type reloadReq struct {
	result chan error
}

// superConfig holds the restart policy and its injected time sources. Zero fields are filled
// with the spec defaults by newSupervisor; tests override maxAttempts/healthStable and inject
// an instant sleeper + a fake clock so the ~6s of real backoff and the 60s stability window
// cost no wall-clock time.
type superConfig struct {
	maxAttempts  int           // total launch attempts before quarantine (spec: 5)
	baseBackoff  time.Duration // full-jitter exponential base (spec: 200ms)
	maxBackoff   time.Duration // backoff cap (spec: 30s)
	healthStable time.Duration // continuous-ready time that forgives prior failures (spec: 60s)
	drainTimeout time.Duration // grace for in-flight calls on reload/stop before the kill (spec: 30s)
	sleep        sleeper       // backoff/drain wait (production realSleep; tests instantSleep)
	now          clock.Clock   // time source for the stability + drain windows (injected like the scheduler)
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

	launches atomic.Int32   // observable spawn count (tests/metrics); incremented before each launch
	curPID   atomic.Int64   // OS pid of the live instance (0 when none); updated on ready + reload swap
	reloadCh chan reloadReq // hot-reload requests; read only while ready or parked in `failed`
	done     chan struct{}  // closed when the run goroutine has fully torn down

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
	if cfg.drainTimeout <= 0 {
		cfg.drainTimeout = 30 * time.Second
	}
	return &supervisor{l: l, name: name, launch: launch, cfg: cfg, reloadCh: make(chan reloadReq), done: make(chan struct{})}
}

// start launches the actor goroutine. It returns immediately; observe progress via state().
func (s *supervisor) start(ctx context.Context) { go s.run(ctx) }

// run is the actor loop and the ONLY goroutine that writes this plugin's state: launch (with
// retry/backoff/quarantine) → serve while ready (crash / stop / hot-reload) → on a crash, count
// it and loop back to relaunch. Launch and backoff block synchronously but are ctx-cancellable,
// so a stop returns promptly.
func (s *supervisor) run(ctx context.Context) {
	defer close(s.done)

	for {
		c := s.launchWithRetry(ctx)
		if c == nil {
			return // context stopped, or quarantined then stopped (the park handles reload)
		}
		if s.serve(ctx, c) == outcomeStopped {
			return
		}
		// serve returned outcomeCrashed: the crash was accounted in afterCrash; loop to relaunch.
	}
}

// serveOutcome is why serve returned: the plugin was stopped (actor should exit) or it crashed
// (actor should relaunch).
type serveOutcome int

const (
	outcomeStopped serveOutcome = iota
	outcomeCrashed
)

// launchWithRetry attempts to launch until the plugin is ready, returning the live conn — or
// nil if the context ended or the plugin quarantined and was then stopped. Each failed launch
// is one attempt against the crash-loop cap.
func (s *supervisor) launchWithRetry(ctx context.Context) *conn {
	for {
		s.transition(StateLaunching)
		s.launches.Add(1)
		c, err := s.launch(ctx)
		if ctx.Err() != nil { // stopped mid-launch
			if c != nil {
				c.kill()
			}
			return nil
		}
		if err == nil {
			return c
		}
		s.cfg.log.Warn("plugin launch failed", "plugin", s.name, "attempt", s.attempts+1, "err", err)
		if s.backoffOrQuarantine(ctx, s.attempts+1 >= s.cfg.maxAttempts) {
			return nil // quarantined then stopped
		}
		// else relaunch
	}
}

// serve holds the plugin in `ready`, routing calls to `cur`, until it crashes, is stopped, or is
// hot-reloaded. A reload launches the NEW instance while the old keeps serving, swaps the
// published client only once the new is ready (no capability gap), then drains+kills the old;
// if the new never becomes ready, the old is retained and the reload reports the error. Reloads
// are handled one at a time on the actor, so any number of them converge to a single live
// instance and a single registry entry.
func (s *supervisor) serve(ctx context.Context, cur *conn) serveOutcome {
	s.becomeReady(cur)
	exited, cancelWatch := s.watch(ctx, cur)

	for {
		select {
		case <-ctx.Done(): // clean stop of a running plugin
			cancelWatch()
			s.teardown(cur)
			return outcomeStopped

		case <-exited: // the process died on its own — a crash
			cancelWatch()
			if s.afterCrash(ctx, cur) {
				return outcomeStopped // quarantined then stopped
			}
			return outcomeCrashed // relaunch

		case req := <-s.reloadCh: // hot-reload: launch new, swap on ready, drain old
			s.launches.Add(1)
			newConn, err := s.launch(ctx)
			if ctx.Err() != nil {
				if newConn != nil {
					newConn.kill()
				}
				req.result <- ctx.Err()
				cancelWatch()
				s.teardown(cur)
				return outcomeStopped
			}
			if err != nil {
				// The new instance never became ready: keep serving the old one, report failure.
				if newConn != nil {
					newConn.kill()
				}
				s.cfg.log.Warn("plugin reload failed; retaining current instance", "plugin", s.name, "err", err)
				req.result <- fmt.Errorf("reload %s: new instance did not become ready: %w", s.name, err)
				continue // cur and its watcher are unchanged
			}
			cancelWatch()
			s.swapTo(newConn)   // publish the new client (state stays ready — no gap), reset readiness clock
			s.drainAndKill(cur) // the old instance admits no new calls after the swap
			cur = newConn
			exited, cancelWatch = s.watch(ctx, cur)
			s.cfg.log.Info("plugin reloaded", "plugin", s.name, "pid", cur.pid)
			req.result <- nil
		}
	}
}

// afterCrash handles a crash of the (now dead) instance: reap the client side, leave ready,
// forgive-or-accumulate the failure counter, then take one attempt against the cap. A crash IS
// a failed attempt (a plugin that launches fine but dies on use must still quarantine), so the
// accounting lives here, not only in launchWithRetry. Returns true if the plugin quarantined
// and was then stopped (the actor should exit).
func (s *supervisor) afterCrash(ctx context.Context, dead *conn) (stop bool) {
	dead.kill() // reap the dead process's client-side resources (idempotent; process already gone)
	s.markUnhealthy()
	// A crash after sustained health forgives the prior loop; a crash within the stability
	// window continues it. Reaching ready is NOT enough — otherwise a plugin that dies every few
	// seconds would reset every cycle and never quarantine.
	if s.cfg.now().Sub(s.readySince) >= s.cfg.healthStable {
		s.attempts = 0
	}
	s.readySince = time.Time{}
	s.curPID.Store(0)
	s.cfg.log.Warn("plugin crashed", "plugin", s.name, "attempt", s.attempts+1)
	return s.backoffOrQuarantine(ctx, s.attempts+1 >= s.cfg.maxAttempts)
}

// watch spawns the process-exit watcher for c and returns the exit channel plus a cancel that
// stops the watcher. Re-created on each reload swap so exactly one watcher runs per live
// instance — no watcher outlives the conn it watches.
func (s *supervisor) watch(ctx context.Context, c *conn) (<-chan struct{}, func()) {
	exited := make(chan struct{})
	wctx, wcancel := context.WithCancel(ctx)
	go func() {
		c.wait(wctx)
		close(exited)
	}()
	return exited, wcancel
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
		// Park in `failed`: a quarantine never clears on its own. The ONLY exits are a stop
		// (ctx) or an explicit operator reload, which resets the counter and relaunches fresh
		// (failed → launching, the sole legal exit). A reload here reports success as soon as the
		// relaunch is initiated, not once ready — unlike a reload of a healthy plugin, which
		// swaps only on ready.
		select {
		case <-ctx.Done():
			return true // stopped
		case req := <-s.reloadCh:
			s.attempts = 0
			s.cfg.log.Info("operator reload leaving quarantine", "plugin", s.name)
			req.result <- nil
			return false // relaunch
		}
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
	s.l.plugins[s.name].cur = c
	s.l.mu.Unlock()
	s.readySince = s.cfg.now()
	s.curPID.Store(int64(c.pid))
}

// swapTo publishes the reloaded instance's client while the state stays `ready`, so no reader
// ever sees a gap — a dispatch straddling the swap resolves to the old client or the new one,
// never nothing. The readiness clock restarts from the new instance, so the stability window
// measures ITS uptime, not the old one's.
func (s *supervisor) swapTo(c *conn) {
	s.l.mu.Lock()
	s.l.plugins[s.name].cur = c
	s.l.mu.Unlock()
	s.readySince = s.cfg.now()
	s.curPID.Store(int64(c.pid))
}

// drainAndKill reaps an instance swapped out by a reload: drain its in-flight calls up to the
// timeout, then kill. Already unpublished by the swap, it receives no new calls, so its in-flight
// count only falls.
func (s *supervisor) drainAndKill(c *conn) {
	s.drain(c)
	c.kill()
}

// drain waits up to drainTimeout for the instance's in-flight calls to finish, so a reload or
// stop does not cut off work about to complete. Any call still running at the deadline is cut
// off by the caller's subsequent kill — the broken connection returns it an error and its
// deferred release frees the in-flight slot. So a plugin that ignores cancellation and outlasts
// the drain cannot leak a slot across the timeout (the port-leak shape). Uses the injected
// clock+sleeper so the poll is real in production and controllable in tests.
func (s *supervisor) drain(c *conn) {
	deadline := s.cfg.now().Add(s.cfg.drainTimeout)
	for c.inflight.Load() > 0 && s.cfg.now().Before(deadline) {
		if err := s.cfg.sleep(context.Background(), 10*time.Millisecond); err != nil {
			return
		}
	}
}

// reload requests a hot-reload and blocks until it resolves: nil once the new instance is ready
// and swapped in, or an error if it never became ready (the old instance is retained). Safe to
// call concurrently — the actor serialises reloads, so N callers converge to one live instance.
func (s *supervisor) reload(ctx context.Context) error {
	req := reloadReq{result: make(chan error, 1)}
	select {
	case s.reloadCh <- req:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errStopped
	}
	select {
	case err := <-req.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return errStopped
	}
}

// currentPID is the OS pid of the live instance (0 when none). Used by tests to assert a reload
// swapped the process and reaped the old one.
func (s *supervisor) currentPID() int { return int(s.curPID.Load()) }

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
	s.l.plugins[s.name].cur = nil
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
	mg.cur = nil
}

// teardown reaps a running plugin on a clean stop: ready → draining → stopped, killing the
// process and withdrawing the client. (#7 makes the resource accounting rigorous; this is the
// state half.)
func (s *supervisor) teardown(c *conn) {
	s.transition(StateDraining)
	s.drain(c)
	c.kill()
	s.l.mu.Lock()
	if err := s.l.plugins[s.name].m.to(StateStopped); err != nil {
		s.cfg.log.Error("illegal plugin state transition", "plugin", s.name, "err", err)
	}
	s.l.plugins[s.name].cur = nil
	s.l.mu.Unlock()
	s.curPID.Store(0)
	// Remove the pidfile so the next boot's sweep does not consider this cleanly-stopped
	// instance an orphan. An UNclean stop (host crash) skips this, leaving the pidfile for the
	// sweep — which is the whole point.
	if c.pidfilePath != "" {
		_ = os.Remove(c.pidfilePath)
	}
}

// state reads the plugin's current state under the loader lock.
func (s *supervisor) state() State {
	s.l.mu.Lock()
	defer s.l.mu.Unlock()
	return s.l.plugins[s.name].m.state
}
