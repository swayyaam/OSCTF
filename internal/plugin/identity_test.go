package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/plugin/pluginpb"
)

type fakeInfoClient struct {
	resp *pluginpb.InfoResponse
	err  error
}

func (f fakeInfoClient) Info(_ context.Context, _ *pluginpb.InfoRequest, _ ...grpc.CallOption) (*pluginpb.InfoResponse, error) {
	return f.resp, f.err
}

// TestVerifyIdentity: the Info cross-check accepts a binary that matches its manifest and rejects
// every way it can diverge — a wrong served type (Info itself errors, the Unimplemented case that is
// the whole point), a type/name/ABI mismatch — while skipping a conn with no Info client (a fake
// launcher) or a supervisor with no declared type (the test-harness path production never takes).
func TestVerifyIdentity(t *testing.T) {
	okInfo := &pluginpb.InfoResponse{Name: "webhook", Type: pluginpb.PluginType_PLUGIN_TYPE_NOTIFICATION, Abi: "1.0"}
	s := &supervisor{name: "webhook", expectType: "notification"}

	if r := s.verifyIdentity(&conn{client: fakeInfoClient{resp: okInfo}}); r != "" {
		t.Errorf("matching identity: want ok, got %q", r)
	}
	// Wrong served type: the dispensed client hits Unimplemented → Info errors. This is the case
	// that would otherwise only surface at the first real call, mid-event.
	if r := s.verifyIdentity(&conn{client: fakeInfoClient{err: errors.New("Unimplemented")}}); r == "" {
		t.Error("Info error (wrong service): want a quarantine reason")
	}
	// Right service answers but reports a different type.
	if r := s.verifyIdentity(&conn{client: fakeInfoClient{resp: &pluginpb.InfoResponse{
		Name: "webhook", Type: pluginpb.PluginType_PLUGIN_TYPE_SCORING, Abi: "1.0",
	}}}); r == "" {
		t.Error("type mismatch: want a quarantine reason")
	}
	// Name mismatch (a mispackaged manifest/binary pair).
	if r := s.verifyIdentity(&conn{client: fakeInfoClient{resp: &pluginpb.InfoResponse{
		Name: "slack", Type: pluginpb.PluginType_PLUGIN_TYPE_NOTIFICATION, Abi: "1.0",
	}}}); r == "" {
		t.Error("name mismatch: want a quarantine reason")
	}
	// ABI major mismatch (binary built against a different ABI than the host).
	if r := s.verifyIdentity(&conn{client: fakeInfoClient{resp: &pluginpb.InfoResponse{
		Name: "webhook", Type: pluginpb.PluginType_PLUGIN_TYPE_NOTIFICATION, Abi: "2.0",
	}}}); r == "" {
		t.Error("ABI mismatch: want a quarantine reason")
	}
	// A conn whose client is not an infoClient (a fake launcher) is not checked.
	if r := s.verifyIdentity(&conn{client: struct{}{}}); r != "" {
		t.Errorf("non-infoClient conn: want skip, got %q", r)
	}
	// No declared type (test-harness supervisor): skipped even with a real client.
	if r := (&supervisor{name: "webhook"}).verifyIdentity(&conn{client: fakeInfoClient{resp: okInfo}}); r != "" {
		t.Errorf("empty expectType: want skip, got %q", r)
	}
}

// TestIdentityMismatchQuarantinesRealSubprocess drives the Info cross-check end to end against a
// REAL plugin subprocess. goodscore serves the SCORING service, but here it is launched as if its
// manifest declared NOTIFICATION — the notification key is dispensed and expectType is
// "notification". The dispensed notification client's Info call hits Unimplemented (the scoring
// binary has no notification service), so the supervisor must quarantine it at LOAD with an
// admin-visible reason — exactly the mid-event failure this check moves to load time. Using a real
// subprocess (not a fake client) proves the gRPC Unimplemented path and the supervisor wiring
// together: a crash-loop quarantine would leave an empty reason, so the reason assertion pins that
// the identity path is what fired.
func TestIdentityMismatchQuarantinesRealSubprocess(t *testing.T) {
	bin := buildDouble(t, "goodscore") // serves SCORING
	l := newLoader()
	l.track("goodscore")
	// Dispense the NOTIFICATION key, as the host would for a manifest declaring type: notification.
	launch := realLaunch(launchSpec{bin: bin, key: KeyNotification, startTimeout: 10 * time.Second, pollInterval: 50 * time.Millisecond})
	s := newSupervisor(l, "goodscore", launch, superConfig{
		maxAttempts:  5,
		baseBackoff:  time.Millisecond,
		maxBackoff:   2 * time.Millisecond,
		healthStable: time.Minute,
		sleep:        instantSleep,
		now:          clock.System(),
	})
	s.expectType = "notification" // the manifest's claim; the binary actually serves scoring

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.start(ctx)

	waitForState(t, s, StateFailed, 20*time.Second)

	snap := l.Snapshot()
	if len(snap) != 1 || snap[0].State != string(StateFailed) {
		t.Fatalf("want the plugin quarantined at load; snapshot=%+v", snap)
	}
	if snap[0].Reason == "" {
		t.Error("quarantine reason is empty — a crash-loop quarantine, not the identity path we expect")
	}
}
