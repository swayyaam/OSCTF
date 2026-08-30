package challengespec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	c, err := ParseFile(path, "pt")
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
	if _, err := ParseFile(path, "bad"); err == nil {
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
	if _, err := ParseFile(path, "bad2"); err == nil {
		t.Fatal("expected error for invalid instancing value")
	}
}

// The CLI promises `{ok:false, field_errors:{…}}` from `challenge validate --json`, which it can
// only produce if a failure names the fields responsible. These pin the attribution — and that
// the message itself is unchanged, since the seeder reports the same text it always has.
func TestValidationErrorNamesTheFields(t *testing.T) {
	cases := []struct {
		name       string
		yaml       string
		dir        string
		wantFields []string
		wantMsg    string
	}{
		{
			name:       "slug must be url-safe",
			yaml:       "slug: Not_A_Slug\ntitle: T\nflag: f\npoints_initial: 100\nscoring: static\n",
			dir:        "Not_A_Slug",
			wantFields: []string{"slug"},
			wantMsg:    `slug "Not_A_Slug" is not url-safe`,
		},
		{
			name:       "slug must match the directory",
			yaml:       "slug: web-login\ntitle: T\nflag: f\npoints_initial: 100\nscoring: static\n",
			dir:        "elsewhere",
			wantFields: []string{"slug"},
			wantMsg:    `slug "web-login" must equal the directory name "elsewhere"`,
		},
		{
			name:       "title and flag are both named",
			yaml:       "slug: web-login\npoints_initial: 100\nscoring: static\n",
			dir:        "web-login",
			wantFields: []string{"title", "flag"},
			wantMsg:    "title and flag are required",
		},
		{
			name:       "dynamic scoring names both missing knobs",
			yaml:       "slug: web-login\ntitle: T\nflag: f\npoints_initial: 100\nscoring: dynamic\n",
			dir:        "web-login",
			wantFields: []string{"points_min", "decay"},
			wantMsg:    "dynamic scoring requires points_min and decay",
		},
		{
			name:       "container requires an image",
			yaml:       "slug: web-login\ntitle: T\nflag: f\npoints_initial: 100\nscoring: static\nkind: container\n",
			dir:        "web-login",
			wantFields: []string{"image", "internal_port"},
			wantMsg:    "container challenges require image and internal_port",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml), tc.dir)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("got %v (%T), want a *ValidationError the CLI can attribute", err, err)
			}
			if ve.Message != tc.wantMsg {
				t.Errorf("message = %q, want %q (the seeder reports this text; it must not drift)", ve.Message, tc.wantMsg)
			}
			if !slices.Equal(ve.Fields, tc.wantFields) {
				t.Errorf("fields = %v, want %v", ve.Fields, tc.wantFields)
			}
			fe := ve.FieldErrors()
			for _, f := range tc.wantFields {
				if len(fe[f]) == 0 {
					t.Errorf("FieldErrors() omits %q: %v", f, fe)
				}
			}
		})
	}
}

// ParseFile and Parse must agree: the CLI validates bytes, the seeder validates a path, and an
// author who gets "valid" from one and a rejection from the other has been told two things.
func TestParseFileAndParseAgree(t *testing.T) {
	const good = "slug: agree\ntitle: T\nflag: osctf{x}\npoints_initial: 100\nscoring: static\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "challenge.yaml")
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, ferr := ParseFile(path, "agree")
	fromBytes, berr := Parse([]byte(good), "agree")
	if (ferr == nil) != (berr == nil) {
		t.Fatalf("disagreement: ParseFile=%v, Parse=%v", ferr, berr)
	}
	// Spec carries a map, so compare the rendered form rather than the struct.
	if fmt.Sprintf("%+v", fromFile) != fmt.Sprintf("%+v", fromBytes) {
		t.Errorf("ParseFile and Parse produced different specs:\n %+v\n %+v", fromFile, fromBytes)
	}
}
