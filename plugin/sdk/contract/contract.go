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

// ChallengeTypeCases specifies the author-supplied inputs VerifyChallengeType checks. A
// challenge-type plugin DECIDES CORRECTNESS, so the contract is behavioural — not just "the RPCs
// answer": provide config the type accepts and rejects, and flags it must accept, reject, and (if
// it can hit an internal failure) refuse to decide.
type ChallengeTypeCases struct {
	// ValidConfig is a config the type accepts. Its NORMALIZED form — what the host stores and later
	// hands back to CheckFlag — is what the flag cases below run against, so the checks exercise
	// production's stored value, not the raw input.
	ValidConfig map[string]string

	// RejectedConfigs are configs the type MUST reject at author time, each with at least one
	// per-field error. At least one is required: a ValidateConfig that returns OK unconditionally (a
	// common author mistake) must fail this verifier.
	RejectedConfigs []map[string]string

	// Correct are submissions CheckFlag must accept — (true, no error). At least one required.
	Correct []string
	// Incorrect are submissions CheckFlag must reject — (false, no error). At least one required.
	Incorrect []string
	// Undecidable are submissions CheckFlag must fail on with an ERROR, not a false. The host fails
	// CLOSED on an error and consumes NO attempt; returning false instead silently burns a player's
	// attempt — the single most consequential thing a checker author can get wrong. Optional: a
	// checker that can always decide (a regex either matches or not) has none, but if yours can hit
	// an internal failure (a missing key, an external call), prove it errors rather than guessing.
	Undecidable []string
}

// VerifyChallengeType launches the challenge-type plugin at binaryPath and asserts the contract that
// matters for a type that decides correctness:
//   - the handshake succeeds and Info advertises the challenge-type with a non-empty name/ABI;
//   - ValidateConfig ACCEPTS ValidConfig and REJECTS each RejectedConfig with a per-field error;
//   - CheckFlag is DETERMINISTIC (same input → same answer; a checker with hidden state is a bug);
//   - CheckFlag accepts Correct, rejects Incorrect, and ERRORS on Undecidable (fail-closed, so no
//     attempt is burned) — all run against the NORMALIZED config the plugin itself returned.
func VerifyChallengeType(tb testing.TB, binaryPath string, cases ChallengeTypeCases) {
	tb.Helper()
	client, rpc := dial(tb, binaryPath)
	defer client.Kill()
	cc := dispense(tb, client, rpc, plugin.KeyChallengeType, "sdk.ChallengeType, …").(pluginpb.ChallengeTypeClient)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := cc.Info(ctx, &pluginpb.InfoRequest{})
	if err != nil {
		tb.Fatalf("Info RPC failed: %v", err)
		return
	}
	if info.GetType() != pluginpb.PluginType_PLUGIN_TYPE_CHALLENGE_TYPE {
		tb.Errorf("Info.Type = %v, want CHALLENGE_TYPE", info.GetType())
	}
	if info.GetName() == "" {
		tb.Error("Info.Name is empty — the host registers the provider by name")
	}
	if info.GetAbi() == "" {
		tb.Error("Info.Abi is empty")
	}

	// Require the properties a bad checker skips, so an author cannot pass by omission.
	if len(cases.RejectedConfigs) == 0 {
		tb.Error("VerifyChallengeType: provide at least one RejectedConfigs — a ValidateConfig that never rejects is untested, and a type that accepts any config is broken")
	}
	if len(cases.Correct) == 0 || len(cases.Incorrect) == 0 {
		tb.Error("VerifyChallengeType: provide at least one Correct and one Incorrect submission — a checker must demonstrate both accepting and rejecting")
	}

	// --- ValidateConfig: accept the valid config, capturing what the host would STORE. ---
	stored := cases.ValidConfig
	vr, err := cc.ValidateConfig(ctx, &pluginpb.ValidateRequest{Config: cases.ValidConfig})
	if err != nil {
		tb.Fatalf("ValidateConfig(ValidConfig) RPC failed: %v", err)
		return
	}
	if !vr.GetOk() {
		tb.Errorf("ValidateConfig rejected ValidConfig %v (field errors %v); it must accept it", cases.ValidConfig, vr.GetFieldErrors())
	}
	if n := vr.GetNormalized(); len(n) > 0 {
		stored = n // the host stores + later passes back the normalized form
	}
	for i, bad := range cases.RejectedConfigs {
		r, err := cc.ValidateConfig(ctx, &pluginpb.ValidateRequest{Config: bad})
		if err != nil {
			tb.Errorf("RejectedConfigs[%d] %v: ValidateConfig RPC failed: %v", i, bad, err)
			continue
		}
		if r.GetOk() {
			tb.Errorf("RejectedConfigs[%d] %v: ValidateConfig ACCEPTED it; it must reject invalid config", i, bad)
			continue
		}
		if len(r.GetFieldErrors()) == 0 {
			tb.Errorf("RejectedConfigs[%d] %v: rejected but with NO per-field error — an admin needs to know which field is wrong", i, bad)
		}
	}

	// --- CheckFlag: against the STORED (normalized) config, twice, to catch hidden state. ---
	checkTwice := func(kind, submitted string) (correct, gotErr bool) {
		resp1, e1 := cc.CheckFlag(ctx, &pluginpb.CheckRequest{Submitted: submitted, Config: stored})
		resp2, e2 := cc.CheckFlag(ctx, &pluginpb.CheckRequest{Submitted: submitted, Config: stored})
		c1, c2 := resp1.GetCorrect(), resp2.GetCorrect()
		if (e1 == nil) != (e2 == nil) || (e1 == nil && c1 != c2) {
			tb.Errorf("%s %q: CheckFlag is not deterministic — got (correct=%v,err=%v) then (correct=%v,err=%v) for the same input (hidden state?)", kind, submitted, c1, e1, c2, e2)
		}
		return c1, e1 != nil
	}
	for _, s := range cases.Correct {
		if correct, gotErr := checkTwice("Correct", s); gotErr {
			tb.Errorf("Correct %q: CheckFlag errored; it must decide (true) for a correct submission", s)
		} else if !correct {
			tb.Errorf("Correct %q: CheckFlag returned false; it must accept a correct submission", s)
		}
	}
	for _, s := range cases.Incorrect {
		if correct, gotErr := checkTwice("Incorrect", s); gotErr {
			tb.Errorf("Incorrect %q: CheckFlag errored; a wrong-but-decidable flag must return false, not an error", s)
		} else if correct {
			tb.Errorf("Incorrect %q: CheckFlag returned true; it must reject an incorrect submission", s)
		}
	}
	for _, s := range cases.Undecidable {
		if _, gotErr := checkTwice("Undecidable", s); !gotErr {
			tb.Errorf("Undecidable %q: CheckFlag returned a decision; when it CANNOT decide it must return an ERROR — the host fails closed and burns no attempt, whereas a false silently costs the player a try", s)
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
