package plugin

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// quietLog returns a logger that discards output, so the boot tests do not spam the run.
func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// placeDouble builds a double and installs it as a plugin under root/<name>/ with a manifest, so
// discovery finds a real, launchable plugin.
func placeDouble(t *testing.T, root, name, double, typ string) {
	t.Helper()
	bin := buildDouble(t, double)
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, double), data, 0o700); err != nil { //nolint:gosec // test binary
		t.Fatal(err)
	}
	manifest := "name: " + name + "\ntype: " + typ + "\nabi: \"1.0\"\nexecutable: " + double + "\n"
	if err := os.WriteFile(filepath.Join(dir, "plugin.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Constraint: a MISSING plugins dir is a silent no-op, not an error — a default deployment has no
// plugins directory and must behave exactly like v0.2 (the standing v0.3 regression gate).
func TestBootMissingPluginsDirIsNoOp(t *testing.T) {
	l := New(Config{Enabled: true, PluginsDir: filepath.Join(t.TempDir(), "absent"), RuntimeDir: t.TempDir(), Log: quietLog()})
	l.Boot(context.Background())
	l.mu.Lock()
	n := len(l.plugins)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("missing plugins dir tracked %d plugins; want 0 (no-op)", n)
	}
}

// Constraint: an EMPTY plugins dir is likewise a silent no-op.
func TestBootEmptyPluginsDirIsNoOp(t *testing.T) {
	l := New(Config{Enabled: true, PluginsDir: t.TempDir(), RuntimeDir: t.TempDir(), Log: quietLog()})
	l.Boot(context.Background())
	l.mu.Lock()
	n := len(l.plugins)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("empty plugins dir tracked %d plugins; want 0 (no-op)", n)
	}
}

// Constraint: Enabled=false skips discovery entirely (pure-core mode == v0.2).
func TestBootDisabledSkipsDiscovery(t *testing.T) {
	root := t.TempDir()
	placeDouble(t, root, "good", "goodscore", "scoring")
	l := New(Config{Enabled: false, PluginsDir: root, RuntimeDir: t.TempDir(), Log: quietLog()})
	l.Boot(context.Background())
	l.mu.Lock()
	n := len(l.plugins)
	l.mu.Unlock()
	if n != 0 {
		t.Errorf("disabled loader tracked %d plugins; want 0", n)
	}
}

// Constraint: Boot must not gate serving. A plugin whose binary never handshakes leaves its
// supervisor stuck in `launching`, but Boot returns immediately (the supervisor does the
// handshake on its own goroutine) — so the core comes up regardless. StartTimeout is short and
// the cap is 1 so cleanup is quick.
func TestBootDoesNotBlockOnLaunchingPlugin(t *testing.T) {
	root := t.TempDir()
	placeDouble(t, root, "stuck", "nohandshake", "scoring")
	l := New(Config{
		Enabled: true, PluginsDir: root, RuntimeDir: t.TempDir(),
		PerPluginCap: 8, StartTimeout: 2 * time.Second, MaxAttempts: 1, Log: quietLog(),
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		sctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		l.Stop(sctx)
		cancel()
	})

	start := time.Now()
	l.Boot(ctx) // must return without waiting for the handshake (StartTimeout)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Boot blocked %s on a plugin that never handshakes — boot must not gate serving", elapsed)
	}

	l.mu.Lock()
	_, tracked := l.plugins["stuck"]
	l.mu.Unlock()
	if !tracked {
		t.Fatal("plugin not tracked after Boot")
	}
	// It is launching, not serving: dispatch to it fails not-ready while the core stays up.
	if err := l.dispatch(context.Background(), "stuck", "Value", func(context.Context, any) error { return nil }); err == nil {
		t.Error("dispatch to a never-ready plugin succeeded; want ErrNotReady")
	}
}
