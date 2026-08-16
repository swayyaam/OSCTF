package plugin

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// A config SECRET (e.g. a webhook URL with an embedded token) is a new class of secret. Like a
// flag or a token, its value must never surface — here on the two host surfaces a config value can
// reach: the admin plugin-view REASON and the boot LOG. Both derive from the resolveConfig error,
// so the redaction is exercised at its single source (not a parallel path).
//
// To prove this bites, temporarily drop the `if decl.Secret` redaction in resolveConfig (echo the
// value like a non-secret): this test then fails on both surfaces. Same discipline as
// -break-scoreboard / the DTO leak guard.
func TestConfigSecretNeverLeaksInReasonOrLog(t *testing.T) {
	const secretVal = "s3cr3t-webhook-TOKEN-abc123"

	var logBuf bytes.Buffer
	l := newLoader()
	l.cfg.Log = slog.New(slog.NewTextHandler(&logBuf, nil))

	// A secret declared int, given a non-int value → the type-error path (the one that would echo a
	// value). The secret comes from the env (the only source for a secret), via the override name.
	m := Manifest{
		Name: "webhook", Type: "notification", ABI: "1.0", Executable: "webhook",
		Config: map[string]ConfigKey{"api_token": {Type: "int", Required: true, Secret: true}},
	}
	t.Setenv(configEnvVar("webhook", "api_token"), secretVal)

	l.launchDiscovered(context.Background(), discovered{manifest: m, executable: "webhook"})

	snap := l.Snapshot()
	if len(snap) != 1 || snap[0].State != string(StateFailed) {
		t.Fatalf("expected the plugin quarantined at load; snapshot=%+v", snap)
	}
	if strings.Contains(snap[0].Reason, secretVal) {
		t.Errorf("config secret leaked into the admin plugin-view reason: %q", snap[0].Reason)
	}
	if strings.Contains(logBuf.String(), secretVal) {
		t.Errorf("config secret leaked into the boot log: %q", logBuf.String())
	}
}
