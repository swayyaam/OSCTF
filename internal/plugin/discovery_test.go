package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func validManifest(name, typ string) string {
	return "name: " + name + "\ntype: " + typ + "\nabi: \"1.0\"\nversion: \"0.1.0\"\nexecutable: bin\n"
}

// writePlugin creates root/<sub>/{plugin.yaml, bin(+x)} and returns root's sub dir.
func writePlugin(t *testing.T, root, sub, manifest string, exec bool) {
	t.Helper()
	dir := filepath.Join(root, sub)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0o600)
	if exec {
		mode = 0o700
	}
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte("#!/bin/sh\n"), mode); err != nil {
		t.Fatal(err)
	}
}

func TestManifestValidate(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{"valid", validManifest("oidc", "auth"), false},
		{"bad name uppercase", validManifest("OIDC", "auth"), true},
		{"bad name leading dash", validManifest("-oidc", "auth"), true},
		{"bad type", validManifest("x", "wat"), true},
		{"abi major mismatch", "name: x\ntype: auth\nabi: \"2.0\"\nexecutable: bin\n", true},
		{"missing executable", "name: x\ntype: auth\nabi: \"1.0\"\n", true},
		{"secret with default", "name: x\ntype: auth\nabi: \"1.0\"\nexecutable: bin\nconfig:\n  s: { secret: true, default: hunter2 }\n", true},
		{"bad config type", "name: x\ntype: auth\nabi: \"1.0\"\nexecutable: bin\nconfig:\n  n: { type: float }\n", true},
		{"unknown field rejected", "name: x\ntype: auth\nabi: \"1.0\"\nexecutable: bin\nwat: 1\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := parseManifest([]byte(c.yaml))
			if err == nil {
				err = m.validate()
			}
			if (err != nil) != c.wantErr {
				t.Errorf("parse+validate err = %v; wantErr = %v", err, c.wantErr)
			}
		})
	}
}

func TestDiscoverPluginsValidSet(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "a-oidc", validManifest("oidc", "auth"), true)
	writePlugin(t, root, "b-elo", validManifest("elo", "scoring"), true)
	writePlugin(t, root, "empty", "", false) // no plugin.yaml written below — remove it
	_ = os.Remove(filepath.Join(root, "empty", "plugin.yaml"))

	got, err := discoverPlugins(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("discovered %d plugins; want 2 (%+v)", len(got), got)
	}
	// Deterministic order: sorted by directory name (a-oidc before b-elo).
	if got[0].manifest.Name != "oidc" || got[1].manifest.Name != "elo" {
		t.Errorf("order = %s,%s; want oidc,elo (sorted by dir)", got[0].manifest.Name, got[1].manifest.Name)
	}
	if got[0].manifest.dispenseKey() != KeyAuth {
		t.Errorf("oidc dispense key = %q; want %q", got[0].manifest.dispenseKey(), KeyAuth)
	}
}

// A name collision between two plugins fails BOTH — neither loads — while unrelated plugins
// survive. Identity in a registry key must never be resolved by load order.
func TestDiscoverPluginsCollisionFailsBoth(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "dir-one", validManifest("oidc", "auth"), true) // same name...
	writePlugin(t, root, "dir-two", validManifest("oidc", "auth"), true) // ...as this one
	writePlugin(t, root, "dir-ok", validManifest("elo", "scoring"), true)

	got, err := discoverPlugins(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].manifest.Name != "elo" {
		t.Fatalf("discovered %+v; want only elo (both oidc claimants dropped)", names(got))
	}
}

func TestDiscoverPluginsSkipsBadOnesKeepsRest(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "good", validManifest("elo", "scoring"), true)
	writePlugin(t, root, "no-manifest", "", false)
	_ = os.Remove(filepath.Join(root, "no-manifest", "plugin.yaml"))
	writePlugin(t, root, "bad-yaml", "name: [oops\n", true)
	writePlugin(t, root, "not-exec", validManifest("nox", "auth"), false) // executable not +x

	got, err := discoverPlugins(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].manifest.Name != "elo" {
		t.Fatalf("discovered %v; want only elo (the bad ones fail alone)", names(got))
	}
}

func TestDiscoverPluginsMissingDir(t *testing.T) {
	got, err := discoverPlugins(filepath.Join(t.TempDir(), "does-not-exist"), nil)
	if err != nil || got != nil {
		t.Errorf("missing dir = (%v, %v); want (nil, nil) — no plugins, no error", got, err)
	}
}

func names(ds []discovered) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d.manifest.Name
	}
	return out
}
