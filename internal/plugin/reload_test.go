package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/swayyaam/OSCTF/internal/clock"
)

// Reload is the operator's way to pick up a changed binary or config without restarting the
// platform. The supervisor's reload path existed since P3-e with NO caller — the machinery was
// built and left unreachable, so "hot-reload on config change" was documented but absent. These
// pin the reachable behaviour.
func TestLoaderReloadLaunchesANewInstance(t *testing.T) {
	l := newLoader()
	l.track("p")
	l.setType("p", "scoring")

	launches := make(chan struct{}, 8)
	launch := func(context.Context) (*conn, error) {
		launches <- struct{}{}
		return &conn{
			client: "fake",
			kill:   func() {},
			wait:   func(wctx context.Context) { <-wctx.Done() },
		}, nil
	}
	s := newSupervisor(l, "p", launch, superConfig{
		maxAttempts: 5, baseBackoff: time.Millisecond, sleep: instantSleep, now: clock.System(),
	})
	l.sups = append(l.sups, s)

	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-s.done }()
	s.start(ctx)
	waitForState(t, s, StateReady, 5*time.Second)

	select {
	case <-launches:
	case <-time.After(5 * time.Second):
		t.Fatal("no initial launch")
	}

	rctx, rcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer rcancel()
	if err := l.Reload(rctx, "p"); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	select {
	case <-launches:
	case <-time.After(5 * time.Second):
		t.Fatal("Reload returned nil but no new instance was launched")
	}
	waitForState(t, s, StateReady, 5*time.Second)
}

// An unknown name is a 404 at the API, so it must be distinguishable here rather than a generic
// failure — otherwise a typo reads to an operator as "the reload broke the plugin".
func TestLoaderReloadUnknownPluginIsDistinguishable(t *testing.T) {
	l := newLoader()
	err := l.Reload(context.Background(), "never-heard-of-it")
	if !errors.Is(err, ErrNoSuchPlugin) {
		t.Fatalf("Reload(unknown) = %v, want ErrNoSuchPlugin", err)
	}
}

// A plugin quarantined at LOAD (bad manifest) is tracked but never got a supervisor. Reloading it
// cannot work, and must say so distinctly instead of hanging or reporting success.
func TestLoaderReloadQuarantinedAtLoadIsNotFound(t *testing.T) {
	l := newLoader()
	l.track("broken")
	l.setType("broken", "scoring")

	err := l.Reload(context.Background(), "broken")
	if !errors.Is(err, ErrNoSuchPlugin) {
		t.Fatalf("Reload(quarantined-at-load) = %v, want ErrNoSuchPlugin", err)
	}
}
