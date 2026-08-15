package plugintest_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.uber.org/goleak"

	"github.com/osctf/platform/internal/plugin/pluginpb"
	"github.com/osctf/platform/internal/plugin/plugintest"
)

// These pin the invariants that CAN be expressed against the P3-a transport + real subprocess
// doubles, before the loader exists. The loader-dependent invariants (state machine, restart/
// quarantine, reload, registry/bus wiring, the in-flight semaphore) are enumerated in
// invariants_pending_test.go with why each needs P3-c/P3-d/P3-e.

func scoreReq() *pluginpb.ScoreRequest {
	return &pluginpb.ScoreRequest{Initial: 500, Min: 100, Decay: 50, Solves: 3}
}

// Transport round-trip: a well-behaved double serves Info + a deterministic Value over a real
// go-plugin subprocess. Proves the P3-a ABI transport works end to end.
func TestDoubleTransportRoundTrip(t *testing.T) {
	c, sc := plugintest.Dial(t, plugintest.Build(t, "goodscore"))
	defer c.Kill()

	info, err := sc.Info(context.Background(), &pluginpb.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.GetName() != "goodscore" || info.GetType() != pluginpb.PluginType_PLUGIN_TYPE_SCORING {
		t.Errorf("Info = %+v; want name goodscore, type SCORING", info)
	}
	res, err := sc.Value(context.Background(), scoreReq())
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if got := res.GetValue(); got != 350 { // 500 - 3*50
		t.Errorf("Value = %d, want 350", got)
	}
}

// ABI major mismatch is refused BEFORE any call (no partial init).
func TestWrongABIRefused(t *testing.T) {
	c, _, err := plugintest.TryDial(plugintest.Build(t, "wrongabi"))
	defer c.Kill()
	if err == nil {
		t.Fatal("a plugin serving a different ABI major was accepted; want the handshake refused")
	}
}

// A crash-on-launch plugin never serves — dialing fails rather than yielding a client.
func TestCrashOnLaunchRefused(t *testing.T) {
	c, _, err := plugintest.TryDial(plugintest.Build(t, "crashlaunch"))
	defer c.Kill()
	if err == nil {
		t.Fatal("a crash-on-launch plugin yielded a client; want the launch to fail")
	}
}

// The host never blocks indefinitely on a hung call: a host-side deadline fires and the caller
// gets a clear DEADLINE_EXCEEDED, fast.
func TestHungCallDeadlineFires(t *testing.T) {
	c, sc := plugintest.Dial(t, plugintest.Build(t, "hang"))
	defer c.Kill()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := sc.Value(ctx, scoreReq())
	elapsed := time.Since(start)

	if status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("hung call err = %v (code %s); want DeadlineExceeded", err, status.Code(err))
	}
	if elapsed > time.Second {
		t.Errorf("hung call took %s to return; the host-side deadline did not free the caller", elapsed)
	}
}

// The SLOW plugin (correct, but 4s, ignoring the ctx): the host-side deadline still fires
// against a non-cooperative plugin, AND a slow call does not block a call to another plugin.
func TestSlowCallHostDeadlineAndIndependence(t *testing.T) {
	slowC, slow := plugintest.Dial(t, plugintest.Build(t, "slow"))
	defer slowC.Kill()
	goodC, good := plugintest.Dial(t, plugintest.Build(t, "goodscore"))
	defer goodC.Kill()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // a slow call in flight, bounded by a 1s host deadline
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		start := time.Now()
		_, err := slow.Value(ctx, scoreReq())
		if status.Code(err) != codes.DeadlineExceeded {
			t.Errorf("slow call err = %v; want DeadlineExceeded (host-side deadline, plugin ignores ctx)", err)
		}
		if e := time.Since(start); e > 2*time.Second {
			t.Errorf("slow call took %s; the 1s host deadline did not fire (plugin sleeps 4s)", e)
		}
	}()

	// Concurrently, a call to the OTHER plugin must return promptly — the slow one does not
	// block it (separate processes; the shared-host in-flight bound is P3-d).
	start := time.Now()
	if _, err := good.Value(context.Background(), scoreReq()); err != nil {
		t.Fatalf("concurrent good call failed: %v", err)
	}
	if e := time.Since(start); e > time.Second {
		t.Errorf("a call to another plugin took %s while a slow call was in flight — it was blocked", e)
	}
	wg.Wait()
}

// A malformed/error response surfaces as a mapped gRPC error, never a silent wrong value.
func TestMalformedErrorSurfaces(t *testing.T) {
	c, sc := plugintest.Dial(t, plugintest.Build(t, "malformed"))
	defer c.Kill()
	_, err := sc.Value(context.Background(), scoreReq())
	if status.Code(err) != codes.Internal {
		t.Errorf("malformed Value err = %v (code %s); want Internal", err, status.Code(err))
	}
}

// A plugin that crashes mid-call: the in-flight call resolves as a clear error (the host does
// not hang on the dead process).
func TestCrashAfterServingErrors(t *testing.T) {
	c, sc := plugintest.Dial(t, plugintest.Build(t, "crashafter"))
	defer c.Kill()
	if _, err := sc.Info(context.Background(), &pluginpb.InfoRequest{}); err != nil {
		t.Fatalf("crashafter should serve Info first: %v", err)
	}
	if _, err := sc.Value(context.Background(), scoreReq()); err == nil {
		t.Error("crashafter Value returned no error though the process died mid-call")
	}
}

// Graceful Kill reaps the child (no zombie); a plugin that ignores SIGTERM is still killed
// (go-plugin escalates), so a stubborn plugin can't leak a process.
func TestKillReapsChild(t *testing.T) {
	for _, name := range []string{"goodscore", "ignoreshutdown"} {
		t.Run(name, func(t *testing.T) {
			c, sc := plugintest.Dial(t, plugintest.Build(t, name))
			if _, err := sc.Info(context.Background(), &pluginpb.InfoRequest{}); err != nil {
				t.Fatalf("%s should serve: %v", name, err)
			}
			c.Kill()
			if !c.Exited() {
				t.Errorf("%s: process still running after Kill() — not reaped", name)
			}
		})
	}
}

// The slow-on-shutdown double serves normally (its drain-timeout behaviour is exercised by the
// loader in P3-d — testing the 45s stall here would just be a 45s wait).
func TestSlowShutdownServesNormally(t *testing.T) {
	c, sc := plugintest.Dial(t, plugintest.Build(t, "slowshutdown"))
	defer c.Kill()
	if _, err := sc.Value(context.Background(), scoreReq()); err != nil {
		t.Fatalf("slowshutdown should serve normally: %v", err)
	}
}

// No goroutine outlives a dial→Kill cycle (the residue guard, extended to the plugin
// transport; full stop-cleanup is P3-c).
func TestNoGoroutineLeakAroundDialKill(t *testing.T) {
	defer goleak.VerifyNone(t,
		// grpc/go-plugin keep a couple of long-lived background goroutines in-process across
		// dials; they are not per-dial leaks.
		goleak.IgnoreTopFunction("google.golang.org/grpc.(*ccBalancerWrapper).watcher"),
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
	)
	c, sc := plugintest.Dial(t, plugintest.Build(t, "goodscore"))
	if _, err := sc.Value(context.Background(), scoreReq()); err != nil {
		t.Fatalf("value: %v", err)
	}
	c.Kill()
}
