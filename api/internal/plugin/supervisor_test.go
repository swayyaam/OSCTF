package plugin

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/plugin/pluginpb"
)

// buildDouble compiles a plugintest double to a temp binary WITHOUT importing the plugintest
// package — plugintest imports THIS package, so an internal (package plugin) test importing it
// would be an import cycle. It shells out to `go build`, so the double (which does import
// plugintest) compiles as a standalone binary and is never linked into this test binary.
func buildDouble(t *testing.T, name string) string {
	t.Helper()
	_, self, _, _ := runtime.Caller(0) // locate doubles/ relative to this file, CWD-independent
	src := filepath.Join(filepath.Dir(self), "plugintest", "doubles", name)
	bin := filepath.Join(t.TempDir(), "double-"+name)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	//nolint:gosec // G204: a test building a known double (fixed source dir) to a temp path.
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build double %q: %v\n%s", name, err, out)
	}
	return bin
}

// waitForState polls the supervisor until it reaches want or the deadline passes.
func waitForState(t *testing.T, s *supervisor, want State, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if s.state() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("plugin did not reach state %s within %s (stuck at %s)", want, within, s.state())
}

// Invariant #4 (crash-loop cap): a plugin that crashes on EVERY launch is retried a bounded
// number of times — the cap — and then quarantined in `failed`, never relaunched without end.
// The backoff is injected-instant so the test exercises the cap logic, not the ~6s of real
// backoff a production crash-loop would take.
func TestCrashOnLaunchQuarantinesAtCap(t *testing.T) {
	bin := buildDouble(t, "crashlaunch")

	var launches atomic.Int32
	base := realLaunch(launchSpec{bin: bin, key: KeyScoring, startTimeout: 10 * time.Second, pollInterval: 50 * time.Millisecond})
	counted := func(ctx context.Context) (*conn, error) {
		launches.Add(1)
		return base(ctx)
	}

	l := newLoader()
	l.track("crashlaunch")
	s := newSupervisor(l, "crashlaunch", counted, superConfig{
		maxAttempts:  5,
		baseBackoff:  time.Millisecond,
		maxBackoff:   2 * time.Millisecond,
		healthStable: time.Minute,
		sleep:        instantSleep,
		now:          clock.System(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.start(ctx)

	waitForState(t, s, StateFailed, 20*time.Second)

	if got := launches.Load(); got != 5 {
		t.Errorf("crashlaunch was launched %d times; want exactly 5 (the cap)", got)
	}
	// It STAYS failed and does not relaunch: give the actor a moment, the count must not grow.
	time.Sleep(150 * time.Millisecond)
	if got := launches.Load(); got != 5 {
		t.Errorf("crashlaunch relaunched past the cap: %d launches total", got)
	}
	if st := s.state(); st != StateFailed {
		t.Errorf("final state = %s; want failed (quarantined)", st)
	}
}

// Invariant #4 (crash-detection gap): deregistration happens on the watcher noticing the exit,
// which lags the actual death by the poll interval. A dispatch in that gap can still reach the
// dead client — it must resolve as a clean, FAST error (a mapped gRPC UNAVAILABLE, or
// ErrNotReady if the watcher already flipped the state), never a host panic and never a hang.
// Pinned with the crashafter double, which dies precisely on the first Value call.
func TestCrashGapDispatchIsCleanNeverHangs(t *testing.T) {
	bin := buildDouble(t, "crashafter")

	l := newLoader()
	l.track("crashafter")
	launch := realLaunch(launchSpec{bin: bin, key: KeyScoring, startTimeout: 10 * time.Second, pollInterval: 50 * time.Millisecond})
	s := newSupervisor(l, "crashafter", launch, superConfig{
		maxAttempts:  5,
		baseBackoff:  time.Millisecond,
		maxBackoff:   2 * time.Millisecond,
		healthStable: time.Minute,
		sleep:        instantSleep,
		now:          clock.System(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.start(ctx)
	waitForState(t, s, StateReady, 15*time.Second)

	value := func(client any) error {
		_, err := client.(pluginpb.ScoringClient).Value(context.Background(),
			&pluginpb.ScoreRequest{Initial: 500, Min: 100, Decay: 50, Solves: 3})
		return err
	}

	// The first Value kills crashafter mid-call: the in-flight call must resolve as an error,
	// not a silent success or a hang.
	if err := l.dispatch("crashafter", value); err == nil {
		t.Error("crashafter Value returned nil though the process died mid-call")
	}

	// A dispatch fired straight into the crash gap must come back non-nil and FAST. Whether it
	// hits the dead connection or a deregistered entry, either answer is clean; a 2s wait means
	// the host blocked on a dead process.
	done := make(chan error, 1)
	go func() { done <- l.dispatch("crashafter", value) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("dispatch into the crash gap returned nil — a dead plugin appeared to serve")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch into the crash gap hung for 2s — the host blocked on a dead process")
	}

	// The host is still alive and responsive — this line executing at all proves no host panic.
	_ = s.state()
}

// fakeClock is a hand-advanced clock, used to make a plugin's "ready duration" exact without
// wall-clock waits (the same seam the scheduler injects for its time-based decisions).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// Invariant #4 (sustained-health reset): the failure counter is forgiven ONLY after the plugin
// stays continuously ready for healthStable — reaching ready is not enough. Driven with a fake
// launcher (each launch hands back a conn the test crashes on command) and an injected clock,
// so a "ready for N seconds" streak is exact and costs no real time.
func TestSustainedHealthGovernsQuarantine(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// setup wires a supervisor over a fake launcher. Each launch pushes a fresh crash channel
	// onto `launched`; closing it crashes that instance. The injected sleeper advances the same
	// clock the stability window reads, so backoff time and ready time share one timeline.
	setup := func() (s *supervisor, clk *fakeClock, launched chan chan struct{}, cancel context.CancelFunc) {
		clk = &fakeClock{t: base}
		launched = make(chan chan struct{}, 64)
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
		l := newLoader()
		l.track("fake")
		s = newSupervisor(l, "fake", launch, superConfig{
			maxAttempts:  5,
			baseBackoff:  time.Second,
			maxBackoff:   30 * time.Second,
			healthStable: 60 * time.Second,
			sleep:        func(_ context.Context, d time.Duration) error { clk.advance(d); return nil },
			now:          clk.now,
		})
		ctx, c := context.WithCancel(context.Background())
		cancel = c
		s.start(ctx)
		return s, clk, launched, cancel
	}

	// nextLaunch waits for the actor to launch again (a relaunch), failing if none comes —
	// a missing relaunch means it quarantined.
	nextLaunch := func(t *testing.T, s *supervisor, launched chan chan struct{}, cycle int) chan struct{} {
		t.Helper()
		select {
		case cr := <-launched:
			return cr
		case <-time.After(5 * time.Second):
			t.Fatalf("cycle %d: no relaunch within 5s (state=%s)", cycle, s.state())
			return nil
		}
	}

	// Every ready streak is shorter than the window, so no crash is forgiven: five sub-window
	// crashes accumulate to the cap and quarantine — the "dies every few seconds" plugin the
	// naive reset-on-ready would loop forever.
	t.Run("crashes_within_window_quarantine", func(t *testing.T) {
		s, clk, launched, cancel := setup()
		defer cancel()
		for i := 0; i < 5; i++ {
			cr := nextLaunch(t, s, launched, i)
			waitForState(t, s, StateReady, 5*time.Second)
			clk.advance(1 * time.Second) // ready for 1s (< 60s window)
			close(cr)                    // crash
		}
		waitForState(t, s, StateFailed, 5*time.Second)
	})

	// Every ready streak exceeds the window, so every crash is forgiven (counter → 0): the
	// plugin restarts indefinitely and never quarantines, well past the cap of 5. This is the
	// exact distinction the naive model got wrong — reaching ready vs. STAYING ready.
	t.Run("sustained_health_never_quarantines", func(t *testing.T) {
		s, clk, launched, cancel := setup()
		defer cancel()
		for i := 0; i < 8; i++ { // past the cap — a naive counter would already have quarantined
			cr := nextLaunch(t, s, launched, i)
			waitForState(t, s, StateReady, 5*time.Second)
			clk.advance(61 * time.Second) // ready for 61s (>= 60s window) → forgiven
			close(cr)
		}
		if st := s.state(); st == StateFailed {
			t.Fatalf("quarantined despite >=60s of health between every crash (state=%s)", st)
		}
	})
}
