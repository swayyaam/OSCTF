package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPluginsDirWritable pins the read-only-plugins-dir detection: a writable dir probes writable
// (→ boot warns), while empty/absent/non-dir and a read-only dir do not (→ no warning). This is
// what converts the "mount the plugins dir read-only" posture from documentation into a detected
// condition at boot.
func TestPluginsDirWritable(t *testing.T) {
	if !pluginsDirWritable(t.TempDir()) {
		t.Error("a writable temp dir must probe writable (boot should warn)")
	}
	if pluginsDirWritable("") {
		t.Error("empty dir must probe not-writable (no warning)")
	}
	if pluginsDirWritable(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Error("absent dir must probe not-writable (no warning)")
	}
	// A file, not a directory, is not a plugins dir.
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pluginsDirWritable(f) {
		t.Error("a non-directory path must probe not-writable")
	}

	// A read-only directory (the recommended posture) must probe NOT writable. Skipped as root,
	// which bypasses mode bits — the check there relies on a genuinely read-only mount, not chmod.
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses mode bits; the read-only case is a real ro mount, not chmod")
	}
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) }) // restore so t.TempDir cleanup can remove it
	if pluginsDirWritable(ro) {
		t.Error("a read-only (0500) dir must probe not-writable (no warning)")
	}
}
