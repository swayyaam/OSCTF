package plugin_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The plugin boundary, checked statically.
//
// Everything under plugin/ is PUBLIC surface: a plugin author imports it from another module, and
// whatever it imports, every plugin in existence inherits. An import of internal/* here is not
// merely untidy — it drags the platform's server-side dependency graph into every plugin binary
// and every plugin's test build.
//
// That is not hypothetical. plugin/sdk once imported internal/plugin (the loader) and
// internal/events, so a hello-world plugin linked the Postgres driver and the Prometheus stack and
// cost 78MB to build. The cross-cutting rule in docs/v0.3/05-first-party-plugins.md said "zero
// imports of internal/*" and was being honoured by the plugin repos while the SDK broke it
// underneath them — nothing checked this side of the boundary. Now something does.
//
// pluginpb is the one permitted exception: it is generated wire code with no dependencies beyond
// protobuf/grpc, and Go's own internal rule already stops an external module importing it directly,
// which is why the SDK wraps it.
const allowedInternal = "github.com/swayyaam/OSCTF/internal/plugin/pluginpb"

func TestPluginPackagesDoNotImportInternal(t *testing.T) {
	const modulePrefix = "github.com/swayyaam/OSCTF/"
	root := "."

	fset := token.NewFileSet()
	var violations []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Test files are excluded on purpose, and the reason is the whole basis of this check: Go
		// never compiles a DEPENDENCY's test files, so an import there cannot reach an author's
		// build. The SDK's own tests legitimately use the host's internal types to check the two
		// sides agree. What an external module actually compiles is the non-test sources — of
		// plugin/sdk when it imports the SDK, and of plugin/sdk/contract when its test imports the
		// harness — so those are exactly what this walks.
		//
		// testdata holds example plugins modelling what an AUTHOR writes; they are held to the
		// same rule, which is the point of having them.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			p, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				continue
			}
			if !strings.HasPrefix(p, modulePrefix+"internal/") {
				continue
			}
			if p == allowedInternal {
				continue
			}
			violations = append(violations, path+" imports "+p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking plugin/: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("public plugin packages must not import internal/* (only %s is allowed).\n"+
			"Every plugin in existence inherits these imports, in its build AND its test build:\n  %s\n"+
			"Move what is needed into a public leaf (see plugin/abi and plugin/eventkeys).",
			allowedInternal, strings.Join(violations, "\n  "))
	}
}

// The other half of the boundary, now that the reference plugins are vendored into this repo at
// plugins/ (they live here only until the org exists; see the Decision log in
// docs/v0.3/05-first-party-plugins.md).
//
// A plugin sitting in the same tree as core can be made to build against the LOCAL sources in two
// ways, and both would make the exit gate meaningless while still passing it: a `replace` pointing
// at the platform, or a go.work file that stitches the modules together implicitly. Either one and
// the plugin no longer proves the published SDK is complete — it proves only that this working
// copy compiles, which is the exact failure the exit criterion exists to catch.
//
// So both are refused here. The gate itself is still run from an out-of-tree copy; this is what
// stops the vendored copies quietly drifting into a state where that gate would pass for the wrong
// reason.
func TestVendoredPluginsDoNotShortCircuitTheExitGate(t *testing.T) {
	root := filepath.Join("..", "plugins")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skip("no vendored plugins in this checkout")
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading %s: %v", root, err)
	}
	checked := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		gomod := filepath.Join(root, e.Name(), "go.mod")
		b, rerr := os.ReadFile(gomod)
		if os.IsNotExist(rerr) {
			continue
		}
		if rerr != nil {
			t.Errorf("reading %s: %v", gomod, rerr)
			continue
		}
		checked++
		for _, line := range strings.Split(string(b), "\n") {
			l := strings.TrimSpace(line)
			if !strings.HasPrefix(l, "replace") {
				continue
			}
			if strings.Contains(l, "github.com/swayyaam/OSCTF") {
				t.Errorf("%s has a replace of the platform:\n  %s\n"+
					"A vendored plugin that builds against the local tree proves nothing about the "+
					"PUBLISHED SDK, which is the whole point of the exit gate. Pin a published "+
					"version instead.", gomod, l)
			}
		}
	}
	if checked == 0 {
		t.Error("no plugin go.mod files were checked — the guard is not actually guarding anything")
	}

	// A go.work would satisfy every plugin's imports from this tree, silently, with no replace
	// line to notice.
	for _, name := range []string{"go.work", "go.work.sum"} {
		if _, err := os.Stat(filepath.Join("..", name)); err == nil {
			t.Errorf("%s exists at the repo root: it would resolve the plugins' platform import "+
				"from the local tree, and the exit gate would pass without proving the published "+
				"SDK is usable", name)
		}
	}
}
