package flags

import (
	"regexp"
	"strings"
	"testing"
)

func TestNewFormatAndPrefix(t *testing.T) {
	g := NewGenerator("osctf")
	shape := regexp.MustCompile(`^osctf\{[0-9a-hjkmnp-tv-z]{39}\}$`)
	f, err := g.New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !shape.MatchString(f) {
		t.Errorf("flag %q does not match the expected shape", f)
	}

	custom, err := NewGenerator("ctf2026").New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.HasPrefix(custom, "ctf2026{") {
		t.Errorf("custom prefix not honored: %q", custom)
	}
}

func TestEmptyPrefixDefaults(t *testing.T) {
	f, err := NewGenerator("").New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.HasPrefix(f, "osctf{") {
		t.Errorf("empty prefix should default to osctf: %q", f)
	}
}

func TestUniqueness(t *testing.T) {
	g := NewGenerator("osctf")
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		f, err := g.New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if seen[f] {
			t.Fatalf("collision at %d: %q", i, f)
		}
		seen[f] = true
	}
}
