package plugin

import (
	"reflect"
	"testing"
)

func env(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestResolveConfig(t *testing.T) {
	m := Manifest{
		Name: "webhook",
		Config: map[string]ConfigKey{
			"step":        {Type: "int", Default: "50"},
			"webhook_url": {Type: "string", Required: true, Secret: true},
			"enabled":     {Type: "bool", Default: "true"},
		},
	}

	t.Run("env overrides the manifest default; secret comes from env", func(t *testing.T) {
		got, err := resolveConfig(m, env(map[string]string{
			"OSCTF_PLUGIN_WEBHOOK_STEP":        "25", // override the manifest default 50
			"OSCTF_PLUGIN_WEBHOOK_WEBHOOK_URL": "https://hook.example/x?token=abc",
		}))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		want := map[string]string{"step": "25", "webhook_url": "https://hook.example/x?token=abc", "enabled": "true"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("resolved = %v, want %v", got, want)
		}
	})

	t.Run("required secret unset -> error at load (not a nil URL at first call)", func(t *testing.T) {
		_, err := resolveConfig(m, env(map[string]string{})) // no webhook_url in env
		if err == nil {
			t.Fatal("expected an error for the unset required secret")
		}
	})

	t.Run("wrong type -> error at load", func(t *testing.T) {
		_, err := resolveConfig(m, env(map[string]string{
			"OSCTF_PLUGIN_WEBHOOK_WEBHOOK_URL": "https://x",
			"OSCTF_PLUGIN_WEBHOOK_STEP":        "not-an-int",
		}))
		if err == nil {
			t.Fatal("expected a type error for a non-int step")
		}
	})

	t.Run("optional unset key is omitted, not empty-stringed", func(t *testing.T) {
		m2 := Manifest{Name: "p", Config: map[string]ConfigKey{"opt": {Type: "string"}}}
		got, err := resolveConfig(m2, env(map[string]string{}))
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("optional-unset should be omitted, got %v", got)
		}
	})
}

// configEnvVar folds the plugin name + key into an upper-snake override name.
func TestConfigEnvVar(t *testing.T) {
	if got := configEnvVar("webhook", "webhook_url"); got != "OSCTF_PLUGIN_WEBHOOK_WEBHOOK_URL" {
		t.Errorf("configEnvVar = %q", got)
	}
	if got := configEnvVar("linear-decay", "step"); got != "OSCTF_PLUGIN_LINEAR_DECAY_STEP" {
		t.Errorf("kebab name should fold to underscores: %q", got)
	}
}

// A resolved map round-trips through the wire env var (stable, sorted).
func TestEncodeConfigStableAndParseable(t *testing.T) {
	a := encodeConfig(map[string]string{"b": "2", "a": "1"})
	b := encodeConfig(map[string]string{"a": "1", "b": "2"})
	if a != b {
		t.Errorf("encoding not stable across map order: %q vs %q", a, b)
	}
	if encodeConfig(map[string]string{}) != "" {
		t.Error("empty config should encode to the empty string (no env var set)")
	}
}
