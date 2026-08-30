package main

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/swayyaam/OSCTF/internal/challengespec"
)

func writeChallenge(t *testing.T, root, slug string, extra map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	body := "slug: " + slug + "\ntitle: T\ncategory: web\nflag: osctf{x}\nscoring: static\npoints_initial: 100\n"
	if len(extra) > 0 {
		body += "files:\n"
		for name := range extra {
			body += "  - " + name + "\n"
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "challenge.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, content := range extra {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A bundle must be byte-identical for identical content — across machines, checkouts, and
// filesystem timestamps. That is what makes it something you can checksum and cache rather than
// a blob that merely happens to work.
func TestBundleIsDeterministic(t *testing.T) {
	files := map[string]string{"a.txt": "alpha", "b.txt": "beta"}

	dir1 := writeChallenge(t, t.TempDir(), "det", files)
	spec1, err := challengespec.ParseFile(filepath.Join(dir1, "challenge.yaml"), "det")
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildBundle(dir1, spec1)
	if err != nil {
		t.Fatal(err)
	}

	// A second copy in a different location, with deliberately different mtimes.
	dir2 := writeChallenge(t, t.TempDir(), "det", files)
	old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	for _, n := range []string{"challenge.yaml", "a.txt", "b.txt"} {
		if err := os.Chtimes(filepath.Join(dir2, n), old, old); err != nil {
			t.Fatal(err)
		}
	}
	spec2, err := challengespec.ParseFile(filepath.Join(dir2, "challenge.yaml"), "det")
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildBundle(dir2, spec2)
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(first, second) {
		t.Fatalf("bundles differ for identical content (%d vs %d bytes) — something machine-specific "+
			"leaked in", len(first), len(second))
	}
}

// Entries are sorted and carry no timestamp or ownership, which is *why* the above holds.
func TestBundleMetadataIsFixed(t *testing.T) {
	dir := writeChallenge(t, t.TempDir(), "meta", map[string]string{"z.txt": "z", "a.txt": "a"})
	spec, err := challengespec.ParseFile(filepath.Join(dir, "challenge.yaml"), "meta")
	if err != nil {
		t.Fatal(err)
	}
	data, err := buildBundle(dir, spec)
	if err != nil {
		t.Fatal(err)
	}

	var names []string
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		// The header is written with an unset ModTime, which USTAR encodes as Unix epoch 0 — so
		// it reads back as a real time, not Go's zero Time. What matters is that it is FIXED and
		// content-independent, which epoch 0 is.
		if h.ModTime.Unix() != 0 {
			t.Errorf("%s carries a real modtime (%v); bundles would differ per checkout", h.Name, h.ModTime)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s carries ownership (%d/%d %q/%q); bundles would differ per machine",
				h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		if h.Mode != 0o644 {
			t.Errorf("%s mode = %o, want 0644 fixed", h.Name, h.Mode)
		}
	}
	want := []string{"a.txt", "challenge.yaml", "z.txt"} // sorted, not directory order
	if len(names) != len(want) {
		t.Fatalf("bundle holds %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("bundle order = %v, want %v (sorted)", names, want)
		}
	}
}

// A spec that points outside its own directory would package someone else's files.
func TestBundleRefusesEscapingPaths(t *testing.T) {
	root := t.TempDir()
	dir := writeChallenge(t, root, "escape", nil)
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := challengespec.ParseFile(filepath.Join(dir, "challenge.yaml"), "escape")
	if err != nil {
		t.Fatal(err)
	}
	spec.Files = []string{"../secret.txt"}
	if _, err := buildBundle(dir, spec); err == nil {
		t.Fatal("a file outside the challenge directory was packaged")
	} else if codeOf(err) != exitValidation {
		t.Errorf("exit = %d, want %d (it is the spec that is wrong)", codeOf(err), exitValidation)
	}
}
