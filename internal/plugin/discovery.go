package plugin

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
)

// discovered is one validated plugin ready to launch: its manifest, its directory, and the
// absolute path to its executable.
type discovered struct {
	manifest   Manifest
	dir        string
	executable string
}

// discoverPlugins scans root one level deep (deterministically, sorted by directory name),
// reads each plugin.yaml, validates it, checks its executable, and returns the launchable set.
// A directory without a plugin.yaml is skipped (debug). A plugin that fails validation fails
// ALONE (logged) — the host and the other plugins still load. A name collision between two
// plugins **fails both** (neither is returned), because a name is a registry key and identity
// must not be resolved by load order.
func discoverPlugins(root string, log *slog.Logger) ([]discovered, error) {
	if log == nil {
		log = slog.Default()
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug("plugins dir absent; no plugins loaded", "dir", root)
			return nil, nil
		}
		return nil, fmt.Errorf("scan plugins dir %s: %w", root, err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var found []discovered
	byName := map[string][]int{} // manifest name -> indices in found (collision detection)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		d, ok := loadOne(dir, log)
		if !ok {
			continue
		}
		byName[d.manifest.Name] = append(byName[d.manifest.Name], len(found))
		found = append(found, d)
	}

	// Fail-both on a name collision: drop every plugin sharing a name with another.
	collided := map[int]bool{}
	for name, idxs := range byName {
		if len(idxs) > 1 {
			dirs := make([]string, 0, len(idxs))
			for _, i := range idxs {
				collided[i] = true
				dirs = append(dirs, found[i].dir)
			}
			log.Error("plugin name collision — failing all claimants; none will load", "name", name, "dirs", dirs)
		}
	}
	if len(collided) == 0 {
		return found, nil
	}
	out := found[:0:0]
	for i, d := range found {
		if !collided[i] {
			out = append(out, d)
		}
	}
	return out, nil
}

// loadOne reads and validates a single plugin dir. Returns ok=false (with a log) for a directory
// with no manifest, an unparseable/invalid manifest, or a missing/non-executable binary — the
// plugin fails alone.
func loadOne(dir string, log *slog.Logger) (discovered, bool) {
	path := filepath.Join(dir, "plugin.yaml")
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is a plugin.yaml under the configured plugins dir.
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug("directory has no plugin.yaml; skipping", "dir", dir)
		} else {
			log.Error("cannot read plugin.yaml; skipping", "dir", dir, "err", err)
		}
		return discovered{}, false
	}
	m, err := parseManifest(data)
	if err != nil {
		log.Error("invalid plugin.yaml; skipping this plugin", "dir", dir, "err", err)
		return discovered{}, false
	}
	if err := m.validate(); err != nil {
		log.Error("plugin manifest failed validation; skipping this plugin", "dir", dir, "name", m.Name, "err", err)
		return discovered{}, false
	}
	exe := filepath.Join(dir, m.Executable)
	if err := checkExecutable(exe); err != nil {
		log.Error("plugin executable unusable; skipping this plugin", "dir", dir, "name", m.Name, "executable", m.Executable, "err", err)
		return discovered{}, false
	}
	return discovered{manifest: m, dir: dir, executable: exe}, true
}

// checkExecutable requires a regular file with an owner-exec bit.
func checkExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm()&0o100 == 0 {
		return fmt.Errorf("%s is not executable (owner-exec bit unset)", path)
	}
	return nil
}

// CountByType reports how many valid plugin manifests of the given type are present on disk.
//
// It exists for one boot-time question the loader cannot otherwise answer in time: whether an
// SSO-only deployment (email login disabled) has any auth plugin at all. Boot is asynchronous by
// design — the core must serve whether or not plugins come up — so at the moment the boot check
// runs, no plugin has registered yet. Gating on REGISTRATION would refuse to start on a timing
// artifact; gating on what is on DISK distinguishes "nothing is configured", which is a real
// misconfiguration worth refusing, from "the plugin has not finished launching", which is normal.
//
// A plugin that is present but later fails to load leaves the deployment with no login. That is
// loud rather than silent: the failure is logged and the plugin shows as failed in the admin view.
func CountByType(root string, ptype string, log *slog.Logger) int {
	found, err := discoverPlugins(root, log)
	if err != nil {
		return 0
	}
	n := 0
	for _, d := range found {
		if d.manifest.Type == ptype {
			n++
		}
	}
	return n
}
