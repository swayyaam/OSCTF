package sdk

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/swayyaam/OSCTF/internal/plugin"
	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
)

// --- notification -----------------------------------------------------------

type recordingNotifier struct {
	got  Event
	fail bool
}

func (r *recordingNotifier) Info() Info              { return Info{Name: "webhook", Version: "1.2.3"} }
func (r *recordingNotifier) Subscriptions() []string { return []string{"challenge.solved"} }
func (r *recordingNotifier) Notify(e Event) error {
	r.got = e
	if r.fail {
		return errors.New("delivery failed")
	}
	return nil
}

func TestNotificationAdapter(t *testing.T) {
	n := &recordingNotifier{}
	a := &notificationAdapter{impl: n}

	info, _ := a.Info(context.Background(), &pluginpb.InfoRequest{})
	if info.GetType() != pluginpb.PluginType_PLUGIN_TYPE_NOTIFICATION || info.GetAbi() != plugin.ABIString {
		t.Errorf("Info type/abi wrong: %v %q", info.GetType(), info.GetAbi())
	}
	subs, _ := a.Subscriptions(context.Background(), &pluginpb.InfoRequest{})
	if len(subs.GetEventNames()) != 1 || subs.GetEventNames()[0] != "challenge.solved" {
		t.Errorf("Subscriptions = %v", subs.GetEventNames())
	}
	ack, err := a.Notify(context.Background(), &pluginpb.Event{Name: "challenge.solved", Id: "e1", Data: map[string]string{"team": "alpha"}})
	if err != nil || !ack.GetHandled() {
		t.Errorf("Notify ok case: ack=%v err=%v", ack.GetHandled(), err)
	}
	if n.got.Name != "challenge.solved" || n.got.Data["team"] != "alpha" {
		t.Errorf("event not translated: %+v", n.got)
	}
	n.fail = true
	if _, err := a.Notify(context.Background(), &pluginpb.Event{Name: "x"}); err == nil {
		t.Error("Notify should propagate a delivery error")
	}
}

// --- challenge type ---------------------------------------------------------

type staticChecker struct{}

func (staticChecker) Info() Info { return Info{Name: "regex", Version: "0.1.0"} }
func (staticChecker) ValidateConfig(c map[string]string) ConfigValidation {
	if c["pattern"] == "" {
		return ConfigValidation{OK: false, FieldErrors: map[string]string{"pattern": "required"}}
	}
	return ConfigValidation{OK: true, Normalized: map[string]string{"pattern": c["pattern"]}}
}
func (staticChecker) CheckFlag(f FlagCheck) (bool, error) {
	return f.Submitted == f.Config["pattern"], nil
}

func TestChallengeTypeAdapter(t *testing.T) {
	a := &challengeTypeAdapter{impl: staticChecker{}}

	info, _ := a.Info(context.Background(), &pluginpb.InfoRequest{})
	if info.GetType() != pluginpb.PluginType_PLUGIN_TYPE_CHALLENGE_TYPE {
		t.Errorf("type = %v", info.GetType())
	}
	if len(info.GetCapabilities()) != 1 || info.GetCapabilities()[0] != "check" {
		t.Errorf("capabilities = %v, want [check] (SDK-derived)", info.GetCapabilities())
	}
	bad, _ := a.ValidateConfig(context.Background(), &pluginpb.ValidateRequest{Config: map[string]string{}})
	if bad.GetOk() || bad.GetFieldErrors()["pattern"] == "" {
		t.Errorf("ValidateConfig should reject empty: %+v", bad)
	}
	ok, _ := a.CheckFlag(context.Background(), &pluginpb.CheckRequest{Submitted: "OSCTF{x}", Config: map[string]string{"pattern": "OSCTF{x}"}})
	if !ok.GetCorrect() {
		t.Error("CheckFlag should accept the matching flag")
	}
}

// --- auth (capability derivation + fail-closed on absent capability) --------

type passwordOnly struct{}

func (passwordOnly) Info() Info { return Info{Name: "local-pw", Version: "1.0.0"} }
func (passwordOnly) Authenticate(id, secret string) (Identity, error) {
	if secret != "hunter2" {
		return Identity{}, errors.New("bad credential")
	}
	return Identity{Subject: id, Username: id}, nil
}

func TestAuthAdapterPasswordOnly(t *testing.T) {
	a := newAuthAdapter(passwordOnly{})

	info, _ := a.Info(context.Background(), &pluginpb.InfoRequest{})
	if info.GetType() != pluginpb.PluginType_PLUGIN_TYPE_AUTH {
		t.Errorf("type = %v", info.GetType())
	}
	caps := info.GetCapabilities()
	if len(caps) != 1 || caps[0] != "password" {
		t.Errorf("capabilities = %v, want [password] (SDK-derived from implemented interfaces)", caps)
	}
	// The redirect capability is absent → its methods must fail closed with Unimplemented.
	if _, err := a.Begin(context.Background(), &pluginpb.BeginRequest{}); status.Code(err) != codes.Unimplemented {
		t.Errorf("Begin without redirect cap = %v, want Unimplemented", err)
	}
	// Password path works.
	id, err := a.Authenticate(context.Background(), &pluginpb.AuthenticateRequest{Identifier: "alice", Secret: "hunter2"})
	if err != nil || id.GetSubject() != "alice" {
		t.Errorf("Authenticate ok case: id=%v err=%v", id, err)
	}
}

func TestServeAuthRejectsNeitherCapability(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Serve(Auth, notAnAuthPlugin) should panic")
		}
	}()
	Serve(Auth, struct{ nonAuth }{})
}

type nonAuth struct{}

func (nonAuth) Info() Info { return Info{} }
