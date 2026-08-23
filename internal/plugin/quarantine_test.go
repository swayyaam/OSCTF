package plugin

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/swayyaam/OSCTF/internal/metrics"
)

// A plugin whose config is invalid must be QUARANTINED at load — visible in the admin Snapshot as
// `failed` with a readable reason and counted — not silently absent. This is what lets an organizer
// whose notifier stopped working see WHY without reading boot logs.
func TestConfigInvalidQuarantinesAtLoadWithReason(t *testing.T) {
	l := newLoader()
	l.cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))

	m := Manifest{
		Name: "webhook", Type: "notification", ABI: "1.0", Executable: "webhook",
		// required secret, and nothing in the env provides it -> resolve fails -> quarantine at load
		Config: map[string]ConfigKey{"webhook_url": {Type: "string", Required: true, Secret: true}},
	}
	l.launchDiscovered(context.Background(), discovered{manifest: m, executable: "webhook"})

	snap := l.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected exactly one tracked plugin, got %d", len(snap))
	}
	s := snap[0]
	if s.Name != "webhook" || s.Type != "notification" {
		t.Errorf("snapshot identity wrong: %+v", s)
	}
	if s.State != string(StateFailed) {
		t.Errorf("state = %q, want %q (quarantined at load, never launched)", s.State, StateFailed)
	}
	if s.Reason == "" || !strings.Contains(s.Reason, "webhook_url") {
		t.Errorf("reason should be readable and name the offending key, got %q", s.Reason)
	}
	if got := metrics.GaugeValue(metrics.PluginLoadFailed.WithLabelValues("webhook")); got != 1 {
		t.Errorf("PluginLoadFailed{webhook} = %v, want 1", got)
	}
}

// A mistyped SECRET must never have its value echoed in the resolve error — the error becomes a log
// line and the quarantine reason, both admin-visible surfaces.
func TestResolveConfigRedactsSecretValueInError(t *testing.T) {
	m := Manifest{Name: "p", Config: map[string]ConfigKey{
		"tok": {Type: "int", Required: true, Secret: true}, // declared int, given a non-int secret
	}}
	_, err := resolveConfig(m, env(map[string]string{"OSCTF_PLUGIN_P_TOK": "s3cr3t-abc-xyz"}))
	if err == nil {
		t.Fatal("expected a type error for the non-int secret")
	}
	if strings.Contains(err.Error(), "s3cr3t-abc-xyz") {
		t.Errorf("secret value leaked into the error: %v", err)
	}
}
