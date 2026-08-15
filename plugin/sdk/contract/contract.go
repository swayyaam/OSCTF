// Package contract lets a plugin author verify a built plugin satisfies the OSCTF contract
// WITHOUT the monorepo — it dials the plugin exactly as the host does, wrapped so no wire type is
// exposed. Call it from a Go test (the template's plugin_test.go does): Build your plugin, then
// Verify* it. Designed alongside the SDK, not bolted on after, so it checks what matters (the
// handshake, the advertised type/ABI, and per-case behaviour) rather than what is convenient.
package contract

import (
	"context"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	hclog "github.com/hashicorp/go-hclog"
	goplugin "github.com/hashicorp/go-plugin"

	"github.com/osctf/platform/internal/plugin"
	"github.com/osctf/platform/internal/plugin/pluginpb"
	"github.com/osctf/platform/plugin/sdk"
)

// Build compiles the plugin whose main package is in dir to a temporary binary and returns its
// path, for passing to a Verify* function. It fails the test on a build error.
func Build(tb testing.TB, dir string) string {
	tb.Helper()
	bin := filepath.Join(tb.TempDir(), "plugin-under-test")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	//nolint:gosec // G204: a contract harness building the author's own plugin dir to a temp path.
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		tb.Fatalf("build plugin in %s: %v\n%s", dir, err, out)
	}
	return bin
}

// ScoringCase is one input and its expected value for VerifyScoring.
type ScoringCase struct {
	Name string
	In   sdk.Score
	Want int
}

// VerifyScoring launches the scoring plugin at binaryPath and asserts the contract: the handshake
// succeeds, Info advertises the scoring type with a non-empty name and ABI, each case's Value
// matches, and the scorer is deterministic (a pure scorer returns the same value for the same
// input). Reports failures through tb, so call it from a Go test.
func VerifyScoring(tb testing.TB, binaryPath string, cases []ScoringCase) {
	tb.Helper()
	client, rpc := dial(tb, binaryPath)
	defer client.Kill()
	sc := dispense(tb, client, rpc, plugin.KeyScoring, "sdk.Scoring, …").(pluginpb.ScoringClient)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := sc.Info(ctx, &pluginpb.InfoRequest{})
	if err != nil {
		tb.Fatalf("Info RPC failed: %v", err)
		return
	}
	if info.GetType() != pluginpb.PluginType_PLUGIN_TYPE_SCORING {
		tb.Errorf("Info.Type = %v, want SCORING", info.GetType())
	}
	if info.GetName() == "" {
		tb.Error("Info.Name is empty — the host registers the provider by name")
	}
	if info.GetAbi() == "" {
		tb.Error("Info.Abi is empty")
	}

	for _, c := range cases {
		resp, err := sc.Value(ctx, scoreReq(c.In))
		if err != nil {
			tb.Errorf("case %q: Value RPC failed: %v", c.Name, err)
			continue
		}
		if int(resp.GetValue()) != c.Want {
			tb.Errorf("case %q: Value = %d, want %d", c.Name, resp.GetValue(), c.Want)
		}
	}

	if len(cases) > 0 {
		in := scoreReq(cases[0].In)
		r1, e1 := sc.Value(ctx, in)
		r2, e2 := sc.Value(ctx, in)
		if e1 == nil && e2 == nil && r1.GetValue() != r2.GetValue() {
			tb.Errorf("scorer is not deterministic: %d != %d for the same input", r1.GetValue(), r2.GetValue())
		}
	}
}

// NotificationCase is one event delivered to the plugin under test by VerifyNotification.
type NotificationCase struct {
	Name  string
	Event sdk.Event
}

// VerifyNotification launches the notification plugin at binaryPath and asserts the contract: the
// handshake succeeds, Info advertises the notification type with a non-empty name and ABI,
// Subscriptions is callable, and each case's event is delivered without a transport error. (It
// does not assert side effects — a notifier's real output is external; the contract is that the
// plugin accepts a subscribed event and does not fail the delivery RPC.)
func VerifyNotification(tb testing.TB, binaryPath string, cases []NotificationCase) {
	tb.Helper()
	client, rpc := dial(tb, binaryPath)
	defer client.Kill()
	nc := dispense(tb, client, rpc, plugin.KeyNotification, "sdk.Notification, …").(pluginpb.NotificationClient)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := nc.Info(ctx, &pluginpb.InfoRequest{})
	if err != nil {
		tb.Fatalf("Info RPC failed: %v", err)
		return
	}
	if info.GetType() != pluginpb.PluginType_PLUGIN_TYPE_NOTIFICATION {
		tb.Errorf("Info.Type = %v, want NOTIFICATION", info.GetType())
	}
	if info.GetName() == "" {
		tb.Error("Info.Name is empty — the host registers the provider by name")
	}
	if info.GetAbi() == "" {
		tb.Error("Info.Abi is empty")
	}
	if _, err := nc.Subscriptions(ctx, &pluginpb.InfoRequest{}); err != nil {
		tb.Errorf("Subscriptions RPC failed: %v", err)
	}

	for _, c := range cases {
		_, err := nc.Notify(ctx, &pluginpb.Event{
			Name:       c.Event.Name,
			Id:         c.Event.ID,
			OccurredAt: c.Event.OccurredAt,
			Data:       c.Event.Data,
		})
		if err != nil {
			tb.Errorf("case %q: Notify(%q) failed: %v", c.Name, c.Event.Name, err)
		}
	}
}

func scoreReq(s sdk.Score) *pluginpb.ScoreRequest {
	return &pluginpb.ScoreRequest{
		Initial: asInt32(s.Initial),
		Min:     asInt32(s.Min),
		Decay:   asInt32(s.Decay),
		Solves:  asInt32(s.Solves),
		Params:  s.Params,
	}
}

// asInt32 saturates a test-case int to the int32 wire range (scoring inputs are small by
// construction; saturating keeps a stray large value bounded rather than sign-flipped).
func asInt32(v int) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	}
	return int32(v) //nolint:gosec // G115: bounds checked immediately above.
}

// dial launches the plugin over the OSCTF handshake — the same path the host uses — and returns
// the client (for Kill) plus the rpc protocol to Dispense from. All go-plugin/pluginpb machinery
// stays in this file, off the public API.
func dial(tb testing.TB, bin string) (*goplugin.Client, goplugin.ClientProtocol) {
	tb.Helper()
	c := goplugin.NewClient(&goplugin.ClientConfig{
		HandshakeConfig: plugin.Handshake,
		Plugins:         plugin.HostPluginSet(),
		//nolint:gosec // G204: launches the built plugin under test; go-plugin owns its lifecycle.
		Cmd:              exec.CommandContext(context.Background(), bin),
		AllowedProtocols: []goplugin.Protocol{goplugin.ProtocolGRPC},
		Logger:           hclog.NewNullLogger(),
	})
	rpc, err := c.Client()
	if err != nil {
		c.Kill()
		tb.Fatalf("dial %s: %v (handshake failed — is this an OSCTF plugin built with plugin/sdk?)", bin, err)
	}
	return c, rpc
}

func dispense(tb testing.TB, c *goplugin.Client, rpc goplugin.ClientProtocol, key, serveHint string) any {
	tb.Helper()
	raw, err := rpc.Dispense(key)
	if err != nil {
		c.Kill()
		tb.Fatalf("dispense %s: %v (does this plugin Serve(%s)?)", key, err, serveHint)
	}
	return raw
}
