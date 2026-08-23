package plugin

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/swayyaam/OSCTF/internal/clock"
	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
)

// openFDCount returns the number of open file descriptors for this process (Linux, via
// /proc/self/fd) or -1 where that is unavailable (macOS dev), so the fd-leak assertion runs on
// the Linux CI runner and is skipped elsewhere.
func openFDCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// goPluginBackgroundGoroutines are grpc/go-plugin's long-lived in-process goroutines that
// persist across dials; they are not per-instance leaks. Same set the plugintest residue guard
// ignores.
func goPluginBackgroundGoroutines() []goleak.Option {
	return []goleak.Option{
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*ccBalancerWrapper).watcher"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
	}
}

// waitProcessDead polls until pid is reaped or the deadline passes.
func waitProcessDead(t *testing.T, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d still alive after %s — a reloaded-away instance leaked", pid, within)
}

// recordingLaunch wraps a launchFn to record the pid of every process it spawns, so a test can
// assert exactly one of them is alive at the end (no leaked old instance).
func recordingLaunch(base launchFn, pids *[]int, mu *sync.Mutex) launchFn {
	return func(ctx context.Context) (*conn, error) {
		c, err := base(ctx)
		if c != nil {
			mu.Lock()
			*pids = append(*pids, c.pid)
			mu.Unlock()
		}
		return c, err
	}
}

// TestMain doubles as the "orphan host" for the boot-sweep test: when OSCTF_ORPHAN_HOST is set,
// this test binary re-execs itself as a minimal plugin host (launch a plugin, write its pidfile,
// block) so the parent can SIGKILL it and observe the child orphaned. The self-exec pattern
// keeps the host inside package plugin, with access to realLaunch/writePidfile, and needs no
// separate binary. Otherwise it just runs the tests.
func TestMain(m *testing.M) {
	if os.Getenv("OSCTF_ORPHAN_HOST") == "1" {
		runOrphanHost() // blocks forever; the parent SIGKILLs us
		return
	}
	os.Exit(m.Run())
}

// runOrphanHost launches a plugin the way the loader does but with parent-death DISABLED
// (noPdeathsig — the macOS path, forced here even on Linux), writes its pidfile, prints
// "READY <pid>", and blocks. When the parent hard-kills this process, the plugin child is
// orphaned exactly as it would be if the platform crashed on macOS/Docker-Desktop.
func runOrphanHost() {
	launch := realLaunch(launchSpec{
		bin:          os.Getenv("OSCTF_ORPHAN_PLUGIN_BIN"),
		key:          KeyScoring,
		name:         "goodscore",
		token:        os.Getenv("OSCTF_ORPHAN_TOKEN"),
		pidfileDir:   os.Getenv("OSCTF_ORPHAN_PIDDIR"),
		noPdeathsig:  true,
		startTimeout: 15 * time.Second,
		pollInterval: time.Second,
	})
	c, err := launch(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "orphan-host launch:", err)
		os.Exit(2)
	}
	// Stdout (not slog) because the parent reads this exact line from our stdout pipe.
	fmt.Fprintf(os.Stdout, "READY %d\n", c.pid)
	select {} // block until SIGKILLed by the parent
}

// containsInt reports whether v is in s.
func containsInt(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// readLineWithin reads one newline-terminated line from r, failing if none arrives in time.
func readLineWithin(t *testing.T, r *bufio.Reader, within time.Duration) string {
	t.Helper()
	type res struct {
		s   string
		err error
	}
	ch := make(chan res, 1)
	go func() { s, err := r.ReadString('\n'); ch <- res{s, err} }()
	select {
	case rr := <-ch:
		if rr.err != nil {
			t.Fatalf("reading orphan-host output: %v", rr.err)
		}
		return strings.TrimSpace(rr.s)
	case <-time.After(within):
		t.Fatalf("timed out after %s waiting for the orphan host's READY line", within)
		return ""
	}
}

// Invariant #1 (boot orphan-sweep as the SOLE mechanism on the no-parent-death path): with
// Pdeathsig disabled (the macOS/Docker-Desktop reality), a hard host crash leaves the plugin
// child ALIVE — nothing in go-plugin reaps it — and only the next boot's pidfile sweep reclaims
// it. This is the dev-path truth, tested end to end with a real orphaned goodscore.
func TestBootSweepReclaimsOrphanedChild(t *testing.T) {
	bin := buildDouble(t, "goodscore")
	dir := t.TempDir()
	token, err := newStartToken()
	if err != nil {
		t.Fatal(err)
	}

	//nolint:gosec // G204: re-execs THIS test binary as the orphan host (see TestMain).
	host := exec.Command(os.Args[0])
	host.Env = append(os.Environ(),
		"OSCTF_ORPHAN_HOST=1",
		"OSCTF_ORPHAN_PLUGIN_BIN="+bin,
		"OSCTF_ORPHAN_PIDDIR="+dir,
		"OSCTF_ORPHAN_TOKEN="+token,
	)
	host.Stderr = os.Stderr
	stdout, err := host.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}

	line := readLineWithin(t, bufio.NewReader(stdout), 30*time.Second) // includes the plugin launch
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[0] != "READY" {
		t.Fatalf("orphan host said %q; want \"READY <pid>\"", line)
	}
	pluginPid, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatalf("bad pid in %q: %v", line, err)
	}

	// Belt-and-braces cleanup: never leak the host or the child if an assertion fails.
	t.Cleanup(func() {
		_ = host.Process.Kill()
		_, _ = host.Process.Wait()
		if processAlive(pluginPid) {
			_ = syscall.Kill(pluginPid, syscall.SIGKILL)
		}
	})

	if !processAlive(pluginPid) {
		t.Fatalf("plugin %d not alive after host READY", pluginPid)
	}

	// Hard-crash the host: SIGKILL, no graceful teardown runs.
	if err := host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_, _ = host.Process.Wait() // reap the host

	// THE CRUX: with no parent-death, the child OUTLIVES its host. If this fails, either
	// go-plugin grew parent-death or the platform did — and the sweep would be redundant, not
	// load-bearing. It is load-bearing.
	time.Sleep(500 * time.Millisecond)
	if !processAlive(pluginPid) {
		t.Fatalf("plugin child %d died with its host — expected an orphan (no parent-death on this path)", pluginPid)
	}

	// The next boot's sweep is the ONLY thing that reclaims it.
	reclaimed, err := sweepOrphans(dir, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !containsInt(reclaimed, pluginPid) {
		t.Errorf("sweep did not reclaim orphan %d (reclaimed=%v)", pluginPid, reclaimed)
	}
	waitProcessDead(t, pluginPid, 3*time.Second)
	if _, statErr := os.Stat(filepath.Join(dir, "goodscore.pid")); !os.IsNotExist(statErr) {
		t.Errorf("pidfile not removed after reclaim: %v", statErr)
	}
}

// Invariant #1 (safety): the sweep kills ONLY a positively-identified child. A pidfile pointing
// at a live pid whose command line does NOT carry the token — a pid recycled by an unrelated
// process — must be refused, never killed. This is what makes token matching load-bearing
// rather than a blind kill-by-pid.
func TestBootSweepRefusesReusedPid(t *testing.T) {
	dir := t.TempDir()

	// A live, unrelated process with no OSCTF token in its command line.
	//nolint:gosec // G204: fixed command, test-controlled.
	victim := exec.Command("sleep", "30")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = victim.Process.Kill(); _, _ = victim.Process.Wait() })
	pid := victim.Process.Pid

	// owner 0 (unknown) so the owner check is skipped and the TOKEN check is what's under test.
	if _, err := writePidfile(dir, "ghost", pid, "osctf-token-that-is-not-in-sleep-argv", 0); err != nil {
		t.Fatal(err)
	}

	reclaimed, err := sweepOrphans(dir, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if containsInt(reclaimed, pid) {
		t.Errorf("sweep killed reused pid %d despite the token mismatch", pid)
	}
	if !processAlive(pid) {
		t.Errorf("sweep killed the unrelated process %d — a token mismatch must be refused", pid)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "ghost.pid")); !os.IsNotExist(statErr) {
		t.Errorf("stale pidfile not cleared")
	}
}

// Invariant #1 (two instances, one host): a boot sweep must not reap a plugin that belongs to a
// SECOND, live platform instance sharing the runtime dir. The plugin carries a valid token (so
// the token check alone would reap it), but its owner platform is alive — the sweep must REFUSE
// and leave the record untouched. Two instances sharing a runtime dir is a deployment mistake;
// the sweep must not turn it into one instance killing the other's plugins.
func TestBootSweepRefusesLivePlatformInstance(t *testing.T) {
	dir := t.TempDir()
	token, err := newStartToken()
	if err != nil {
		t.Fatal(err)
	}

	// The "plugin": a live process whose argv carries the token (so processMatchesToken matches,
	// as a real plugin's would). perl passes trailing args through to @ARGV, ignored.
	//nolint:gosec // G204: fixed command, test-controlled; token is a hex string.
	plugin := exec.Command("perl", "-e", "sleep 60", "--", token)
	if err := plugin.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plugin.Process.Kill(); _, _ = plugin.Process.Wait() })

	// The OWNER: a live process standing in for a second platform instance.
	//nolint:gosec // G204: fixed command, test-controlled.
	owner := exec.Command("sleep", "60")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Process.Kill(); _, _ = owner.Process.Wait() })

	if _, err := writePidfile(dir, "other-instance-plugin", plugin.Process.Pid, token, owner.Process.Pid); err != nil {
		t.Fatal(err)
	}

	reclaimed, err := sweepOrphans(dir, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if containsInt(reclaimed, plugin.Process.Pid) {
		t.Errorf("sweep reaped pid %d belonging to a live platform instance", plugin.Process.Pid)
	}
	if !processAlive(plugin.Process.Pid) {
		t.Errorf("sweep killed another instance's live plugin — the owner check failed")
	}
	// The record is not ours to remove either: leave the other instance's bookkeeping intact.
	if _, statErr := os.Stat(filepath.Join(dir, "other-instance-plugin.pid")); os.IsNotExist(statErr) {
		t.Errorf("sweep removed another live instance's pidfile")
	}
}

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

	value := func(ctx context.Context, client any) error {
		_, err := client.(pluginpb.ScoringClient).Value(ctx,
			&pluginpb.ScoreRequest{Initial: 500, Min: 100, Decay: 50, Solves: 3})
		return err
	}

	// The first Value kills crashafter mid-call: the in-flight call must resolve as an error,
	// not a silent success or a hang.
	if err := l.dispatch(context.Background(), "crashafter", "Value", value); err == nil {
		t.Error("crashafter Value returned nil though the process died mid-call")
	}

	// A dispatch fired straight into the crash gap must come back non-nil and FAST. Whether it
	// hits the dead connection or a deregistered entry, either answer is clean; a 2s wait means
	// the host blocked on a dead process.
	done := make(chan error, 1)
	go func() { done <- l.dispatch(context.Background(), "crashafter", "Value", value) }()
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

// value dispatches a Value call to a scoring plugin and checks the deterministic result, so a
// test can assert the plugin still serves after a reload.
func value(t *testing.T, l *Loader, name string) error {
	t.Helper()
	return l.dispatch(context.Background(), name, "Value", func(ctx context.Context, client any) error {
		r, err := client.(pluginpb.ScoringClient).Value(ctx,
			&pluginpb.ScoreRequest{Initial: 500, Min: 100, Decay: 50, Solves: 3})
		if err != nil {
			return err
		}
		if got := r.GetValue(); got != 350 { // 500 - 3*50
			t.Errorf("Value = %d after reload; want 350", got)
		}
		return nil
	})
}

// Invariant #5 (reload idempotent): reloading a healthy plugin replaces its process and reaps
// the old one — one live process, one registry entry, no leak — and the plugin keeps serving
// across the swap. Real goodscore subprocesses; pids prove the process actually changed and the
// old one died.
func TestReloadReplacesProcessAndReapsOld(t *testing.T) {
	defer goleak.VerifyNone(t, goPluginBackgroundGoroutines()...)

	bin := buildDouble(t, "goodscore")
	l := newLoader()
	l.track("goodscore")
	var mu sync.Mutex
	var pids []int
	launch := recordingLaunch(
		realLaunch(launchSpec{bin: bin, key: KeyScoring, startTimeout: 10 * time.Second, pollInterval: 50 * time.Millisecond}),
		&pids, &mu)
	s := newSupervisor(l, "goodscore", launch, superConfig{sleep: instantSleep, now: clock.System()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.start(ctx)
	waitForState(t, s, StateReady, 15*time.Second)

	pid0 := s.currentPID()
	if pid0 <= 0 {
		t.Fatalf("no live pid after ready")
	}

	// Two reloads in a row: each must swap in a new process and reap the previous one.
	if err := s.reload(ctx); err != nil {
		t.Fatalf("reload 1: %v", err)
	}
	pid1 := s.currentPID()
	if pid1 == pid0 {
		t.Errorf("reload 1 did not replace the process (pid still %d)", pid0)
	}
	waitProcessDead(t, pid0, 3*time.Second)
	if s.state() != StateReady {
		t.Errorf("state after reload 1 = %s; want ready (no capability gap)", s.state())
	}

	if err := s.reload(ctx); err != nil {
		t.Fatalf("reload 2: %v", err)
	}
	pid2 := s.currentPID()
	if pid2 == pid1 {
		t.Errorf("reload 2 did not replace the process (pid still %d)", pid1)
	}
	waitProcessDead(t, pid1, 3*time.Second)

	// One registry entry, and the plugin still serves the correct value across the swaps.
	l.mu.Lock()
	n := len(l.plugins)
	l.mu.Unlock()
	if n != 1 {
		t.Errorf("registry has %d entries after reloads; want 1", n)
	}
	if err := value(t, l, "goodscore"); err != nil {
		t.Errorf("dispatch after reloads: %v", err)
	}

	// Exactly one of every process we launched is still alive — the current one.
	mu.Lock()
	spawned := append([]int(nil), pids...)
	mu.Unlock()
	alive := 0
	for _, p := range spawned {
		if processAlive(p) {
			alive++
		}
	}
	if alive != 1 {
		t.Errorf("%d of %d launched processes still alive; want exactly 1 (no leaked old process)", alive, len(spawned))
	}
	if !processAlive(pid2) {
		t.Errorf("the current pid %d is not alive", pid2)
	}

	cancel()
	<-s.done
	waitProcessDead(t, pid2, 3*time.Second)
}

// Invariant #5 (idempotent under concurrency): many reloads fired at once still converge to ONE
// live process and one registry entry — the actor serialises them, so there is no window where
// two instances survive.
func TestReloadConcurrentConvergesToOneInstance(t *testing.T) {
	defer goleak.VerifyNone(t, goPluginBackgroundGoroutines()...)

	bin := buildDouble(t, "goodscore")
	l := newLoader()
	l.track("goodscore")
	var mu sync.Mutex
	var pids []int
	launch := recordingLaunch(
		realLaunch(launchSpec{bin: bin, key: KeyScoring, startTimeout: 10 * time.Second, pollInterval: 50 * time.Millisecond}),
		&pids, &mu)
	s := newSupervisor(l, "goodscore", launch, superConfig{sleep: instantSleep, now: clock.System()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.start(ctx)
	waitForState(t, s, StateReady, 15*time.Second)

	const k = 5
	var wg sync.WaitGroup
	errs := make([]error, k)
	for i := 0; i < k; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = s.reload(ctx) }(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Errorf("concurrent reload %d: %v", i, e)
		}
	}

	cur := s.currentPID()
	if cur <= 0 || !processAlive(cur) {
		t.Fatalf("no live current process after concurrent reloads (pid=%d)", cur)
	}
	// Every other process we spawned must be reaped — no two instances survive a reload race.
	mu.Lock()
	spawned := append([]int(nil), pids...)
	mu.Unlock()
	for _, p := range spawned {
		if p != cur {
			waitProcessDead(t, p, 3*time.Second)
		}
	}
	l.mu.Lock()
	n := len(l.plugins)
	l.mu.Unlock()
	if n != 1 {
		t.Errorf("registry has %d entries; want 1", n)
	}
	if err := value(t, l, "goodscore"); err != nil {
		t.Errorf("dispatch after concurrent reloads: %v", err)
	}

	cancel()
	<-s.done
	waitProcessDead(t, cur, 3*time.Second)
}

// loadServeStop runs one full lifecycle — launch a real goodscore, serve a call, then stop —
// and returns the pid, asserting the process is reaped and the plugin ends `stopped`. The unit
// the residue guard repeats to prove nothing accumulates per cycle.
func loadServeStop(t *testing.T, bin string) int {
	t.Helper()
	l := newLoader()
	l.track("goodscore")
	launch := realLaunch(launchSpec{bin: bin, key: KeyScoring, startTimeout: 10 * time.Second, pollInterval: 50 * time.Millisecond})
	s := newSupervisor(l, "goodscore", launch, superConfig{sleep: instantSleep, now: clock.System()})

	ctx, cancel := context.WithCancel(context.Background())
	s.start(ctx)
	waitForState(t, s, StateReady, 15*time.Second)
	pid := s.currentPID()
	if err := value(t, l, "goodscore"); err != nil {
		cancel()
		t.Fatalf("serve: %v", err)
	}

	cancel()
	<-s.done
	if st := s.state(); st != StateStopped {
		t.Errorf("state after stop = %s; want stopped", st)
	}
	waitProcessDead(t, pid, 3*time.Second) // child killed AND reaped — no zombie
	return pid
}

// Invariant #7 (full-stop cleanup): no goroutine, socket, or child outlives a stopped plugin.
// goleak (deferred, after the last stop) proves the actor + watcher + gRPC-client goroutines
// are gone; the per-cycle reap proves no child/zombie; the fd count across repeated cycles
// proves no socket/pipe accumulates (the residue guard, extended to plugins). A warmup cycle
// establishes gRPC's shared background before the baseline so it is not counted as growth.
func TestFullStopReclaimsGoroutinesFDsAndChild(t *testing.T) {
	defer goleak.VerifyNone(t, goPluginBackgroundGoroutines()...)

	bin := buildDouble(t, "goodscore")

	loadServeStop(t, bin) // warmup: establish gRPC's shared background goroutines/fds
	baseline := openFDCount()

	const cycles = 3
	for i := 0; i < cycles; i++ {
		loadServeStop(t, bin)
	}
	after := openFDCount()

	// -1 means non-Linux (no /proc); the fd assertion only runs where the count is available.
	if baseline >= 0 && after > baseline+4 {
		t.Errorf("open fds grew across %d load→serve→stop cycles: baseline=%d after=%d — a socket/pipe leaked per stop",
			cycles, baseline, after)
	}
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
