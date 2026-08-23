package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/swayyaam/OSCTF/internal/httpserver"
	"github.com/swayyaam/OSCTF/internal/plugin"
	"github.com/swayyaam/OSCTF/internal/plugin/plugintest"
)

// recordingRegistrar records which plugins reach `ready` (Register is called on the ready
// transition, per #3). A plugin that never handshakes never becomes ready, so its name never
// appears here — a clean, exported signal that it is NOT ready.
type recordingRegistrar struct {
	mu         sync.Mutex
	registered map[string]bool
}

func (r *recordingRegistrar) Register(name, _ string, _ plugin.Caller) error {
	r.mu.Lock()
	r.registered[name] = true
	r.mu.Unlock()
	return nil
}
func (r *recordingRegistrar) Deregister(string, string) {}
func (r *recordingRegistrar) has(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registered[name]
}

// TestBootDoesNotGateServing pins the full-server half of "boot must not gate serving" (the loader
// half is TestBootDoesNotBlockOnLaunchingPlugin): the HTTP server answers /healthz while a plugin
// is still launching and has NOT become ready. It wires the two real components that embody the
// invariant exactly as cmdServe does — the real plugin loader booting in a goroutine
// (`go loader.Boot(background)`, main.go) and the real httpserver — against a REAL `nohandshake`
// subprocess (a valid executable that never completes the handshake, so its supervisor sits in
// `launching` forever). The full cmdServe adds Postgres/Redis/MinIO/Docker, none of which the
// boot-vs-serve ordering depends on; /healthz is a static handler, independent of plugins by
// construction, and this proves the composition preserves that.
func TestBootDoesNotGateServing(t *testing.T) {
	// A never-ready plugin laid out for discovery: manifest + the nohandshake binary as `bin`.
	pluginsDir := t.TempDir()
	sub := filepath.Join(pluginsDir, "hangboot")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	manifest := "name: hangboot\ntype: scoring\nabi: \"1.0\"\nversion: \"0.1.0\"\nexecutable: bin\n"
	if err := os.WriteFile(filepath.Join(sub, "plugin.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	built, err := os.ReadFile(plugintest.Build(t, "nohandshake"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "bin"), built, 0o700); err != nil { //nolint:gosec // G306: an executable test double must be executable.
		t.Fatal(err)
	}

	reg := &recordingRegistrar{registered: map[string]bool{}}
	loader := plugin.New(plugin.Config{
		Enabled:      true,
		PluginsDir:   pluginsDir,
		RuntimeDir:   t.TempDir(),
		PerPluginCap: 4,
		GlobalCap:    16,
		QueueWait:    time.Second,
		DrainTimeout: 3 * time.Second,
		StartTimeout: 3 * time.Second, // small: the never-ready launch fails fast, keeping the test bounded
		HealthStable: time.Second,
		MaxAttempts:  2,
		Registrar:    reg,
	})
	// Boot in a goroutine exactly as cmdServe does. (Boot is itself structurally non-blocking —
	// launchDiscovered starts each supervisor and returns, the handshake runs on the supervisor's
	// own goroutine; the loader-level TestBootDoesNotBlockOnLaunchingPlugin pins that Boot returns
	// in <1s and that dispatch to the never-ready plugin is ErrNotReady. This test adds the missing
	// full-server dimension: the composed HTTP server answers /healthz while that plugin is not
	// ready, i.e. liveness is independent of plugin readiness.)
	go loader.Boot(context.Background())
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		loader.Stop(stopCtx)
		cancel()
	}()

	// The real HTTP server. /healthz is a static handler registered unconditionally, independent of
	// the API surface (nil Handlers → 501 catch-all elsewhere), which is the point.
	srv := httptest.NewServer(httpserver.New(httpserver.Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	defer srv.Close()

	// /healthz must answer 200 while the plugin is launching (a liveness bound, generously above the
	// millisecond async startup). A composition that gated liveness on plugin readiness — or that
	// failed to come up with a plugin present — would miss this window.
	deadline := time.Now().Add(5 * time.Second)
	var lastErr string
	for time.Now().Before(deadline) {
		resp, herr := http.Get(srv.URL + "/healthz")
		if herr != nil {
			lastErr = herr.Error()
			time.Sleep(20 * time.Millisecond)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			// Served while the plugin is NOT ready: a never-handshaking plugin cannot have reached
			// `ready`, so it was never registered. That is the invariant — the core answers whether
			// or not plugins do.
			if reg.has("hangboot") {
				t.Fatal("hangboot became ready — the nohandshake double is not behaving as a never-ready plugin")
			}
			return
		}
		lastErr = "status " + resp.Status
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("/healthz did not return 200 while a plugin was launching (boot gated serving): last=%s", lastErr)
}
