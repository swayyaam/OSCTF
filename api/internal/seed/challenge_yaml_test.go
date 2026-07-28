package seed

import (
	"os"
	"path/filepath"
	"testing"
)

func writeYAML(t *testing.T, dir, name, body string) string {
	t.Helper()
	chalDir := filepath.Join(dir, name)
	if err := os.MkdirAll(chalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chalDir, "challenge.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParsePerTeamContainer(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "pt", `
slug: pt
title: PT
category: web
flag: "OSCTF{x}"
scoring: static
points_initial: 100
visible: true
kind: container
image: osctf/x:0.2
internal_port: 8000
instancing: per_team
flag_mode: per_instance
instance_ttl_seconds: 1800
egress: false
writable_paths: [/data]
`)
	c, err := parseChallengeYAML(path, "pt")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Instancing != "per_team" || c.FlagMode != "per_instance" {
		t.Errorf("instancing/flag_mode = %q/%q", c.Instancing, c.FlagMode)
	}
	if c.InstanceTTLSeconds == nil || *c.InstanceTTLSeconds != 1800 {
		t.Errorf("ttl = %v", c.InstanceTTLSeconds)
	}
	if c.Egress == nil || *c.Egress {
		t.Errorf("egress = %v, want false", c.Egress)
	}
	if len(c.WritablePaths) != 1 || c.WritablePaths[0] != "/data" {
		t.Errorf("writable_paths = %v", c.WritablePaths)
	}
}

func TestParseRejectsPerTeamOnStandard(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "bad", `
slug: bad
title: Bad
category: misc
flag: "OSCTF{x}"
scoring: static
points_initial: 100
visible: true
instancing: per_team
`)
	if _, err := parseChallengeYAML(path, "bad"); err == nil {
		t.Fatal("expected error for per_team on a standard challenge")
	}
}

func TestParseRejectsBadInstancing(t *testing.T) {
	dir := t.TempDir()
	path := writeYAML(t, dir, "bad2", `
slug: bad2
title: Bad2
category: web
flag: "OSCTF{x}"
scoring: static
points_initial: 100
visible: true
kind: container
image: osctf/x:0.2
internal_port: 8000
instancing: nonsense
`)
	if _, err := parseChallengeYAML(path, "bad2"); err == nil {
		t.Fatal("expected error for invalid instancing value")
	}
}
