package plugin

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/osctf/platform/internal/clock"
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
