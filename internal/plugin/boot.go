package plugin

import (
	"context"
	"log/slog"
	"os"
	"time"
)

// Config is what the composition root hands the loader at startup. The budget fields come from
// the shared fd accountant (GlobalCap) + OSCTF_PLUGIN_MAX_INFLIGHT (PerPluginCap); the rest are
// discovery and supervision knobs.
type Config struct {
	Enabled      bool          // false skips discovery entirely (pure-core mode, == v0.2 behaviour)
	PluginsDir   string        // OSCTF_PLUGINS_DIR — scanned one level deep
	RuntimeDir   string        // OSCTF_RUNTIME_DIR — where pidfiles are written / the boot sweep reads
	PerPluginCap int           // OSCTF_PLUGIN_MAX_INFLIGHT (per plugin)
	GlobalCap    int           // shared fd-accountant grant (across all plugins); 0 = unbounded
	QueueWait    time.Duration // OSCTF_PLUGIN_QUEUE_WAIT
	DrainTimeout time.Duration // OSCTF_PLUGIN_DRAIN_TIMEOUT
	StartTimeout time.Duration // handshake wait before a launch is deemed failed
	HealthStable time.Duration // OSCTF_PLUGIN_HEALTH_STABLE
	MaxAttempts  int           // OSCTF_PLUGIN_RESTART_CAP
	Log          *slog.Logger
	Registrar    Registrar // wires ready plugins into their type registries; nil = no-op (transport only)
}

// New builds a loader from Config, wiring the two-level in-flight budget. Discovery and launch
// happen in Boot; call it in a goroutine so a slow sweep or a plugin that never handshakes cannot
// hold up serving.
func New(cfg Config) *Loader {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	perPlugin := cfg.PerPluginCap
	if perPlugin <= 0 {
		perPlugin = 64
	}
	qw := cfg.QueueWait
	if qw <= 0 {
		qw = time.Second
	}
	return &Loader{
		plugins: map[string]*managed{},
		budget:  newBudget(perPlugin, cfg.GlobalCap, qw),
		cfg:     cfg,
	}
}

// Boot reclaims orphaned plugin children, discovers plugins, and launches a supervisor for each.
// It NEVER gates serving: the sweep runs before any launch (so this run's fresh pidfiles are not
// mistaken for orphans), discovery is a bounded one-level scan, and each launch is handed to a
// supervisor goroutine that does the handshake — so a plugin stuck launching, a binary that never
// handshakes, or a slow sweep leaves the core answering. Missing or empty OSCTF_PLUGINS_DIR is a
// silent no-op (a default deployment has no plugins and must behave exactly like v0.2). main runs
// Boot in a goroutine so even a slow sweep does not delay the HTTP server.
func (l *Loader) Boot(ctx context.Context) {
	if !l.cfg.Enabled {
		l.cfg.Log.Info("plugins disabled; skipping discovery (pure-core mode)")
		return
	}

	if l.cfg.RuntimeDir != "" {
		if reclaimed, err := sweepOrphans(l.cfg.RuntimeDir, l.cfg.Log); err != nil {
			l.cfg.Log.Warn("boot orphan sweep failed; continuing", "err", err)
		} else if len(reclaimed) > 0 {
			l.cfg.Log.Info("boot orphan sweep reclaimed leaked plugin children", "count", len(reclaimed))
		}
	}

	// A plugins directory the process can WRITE to is a persistence path: a compromised core could
	// drop a plugin binary + manifest there for the next boot to launch AS the platform. The
	// recommended posture is a read-only mount (docs/v0.3/03-plugin-loader.md); detect the writable
	// case loudly rather than only documenting it — the same shape as the reverse-proxy misconfig
	// warning. Runs whenever plugins are enabled, even before the first plugin exists (the risk is
	// dropping the FIRST one).
	if pluginsDirWritable(l.cfg.PluginsDir) {
		l.cfg.Log.Warn("SECURITY: the plugins directory is WRITABLE by the platform process — a "+
			"compromised core could drop a plugin binary + manifest there for the next boot to launch "+
			"AS the platform. Mount it READ-ONLY to the process (docs/v0.3/03-plugin-loader.md).",
			"dir", l.cfg.PluginsDir)
	}

	found, err := discoverPlugins(l.cfg.PluginsDir, l.cfg.Log)
	if err != nil {
		l.cfg.Log.Error("plugin discovery failed; continuing with no plugins", "err", err)
		return
	}
	if len(found) == 0 {
		l.cfg.Log.Info("no plugins discovered", "dir", l.cfg.PluginsDir)
		return
	}

	bctx, cancel := context.WithCancel(ctx)
	l.mu.Lock()
	l.bootCancel = cancel
	l.mu.Unlock()

	for _, d := range found {
		l.launchDiscovered(bctx, d)
	}
	l.cfg.Log.Info("plugins launched", "count", len(found))
}

// pluginsDirWritable reports whether THIS PROCESS can create a file in dir. It probes by creating
// and immediately removing a temp file — the ground truth for "can I write here", which a stat of
// the mode bits alone would miss: a read-only bind mount leaves the bits writable while the mount
// rejects the write, and that ro mount is exactly the recommended posture. An empty, absent, or
// non-directory path returns false (nothing to warn about; an absent dir is the silent no-op the
// loader already treats as pure-core).
func pluginsDirWritable(dir string) bool {
	if dir == "" {
		return false
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false
	}
	f, err := os.CreateTemp(dir, ".osctf-write-probe-*")
	if err != nil {
		return false // read-only (or otherwise unwritable) → the desired posture, no warning
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return true
}

// launchDiscovered tracks a discovered plugin and starts its supervisor. The supervisor goroutine
// performs the handshake, so this returns immediately — a plugin that never becomes ready never
// blocks Boot.
func (l *Loader) launchDiscovered(ctx context.Context, d discovered) {
	token, err := newStartToken()
	if err != nil {
		l.cfg.Log.Error("could not mint a start token; skipping plugin", "name", d.manifest.Name, "err", err)
		return
	}
	// Resolve + validate config against the manifest schema BEFORE launching (fail at load, not at
	// first call). A missing required key or a wrong-typed value quarantines the plugin here: it is
	// not launched and never reaches ready, with a clear, actionable message — better than a plugin
	// that starts and then fails every request because a URL was empty.
	cfg, err := resolveConfig(d.manifest, nil)
	if err != nil {
		// Fail at LOAD: quarantine with a readable reason (failed state + metric + admin view),
		// never launch. The reason carries no secret value (resolveConfig redacts those).
		l.failLoad(d.manifest.Name, d.manifest.Type, err.Error())
		return
	}
	spec := launchSpec{
		bin:          d.executable,
		key:          d.manifest.dispenseKey(),
		name:         d.manifest.Name,
		token:        token,
		pidfileDir:   l.cfg.RuntimeDir,
		startTimeout: l.cfg.StartTimeout,
		pollInterval: 250 * time.Millisecond,
		configEnv:    encodeConfig(cfg),
	}
	l.track(d.manifest.Name)
	l.setType(d.manifest.Name, d.manifest.Type)
	sup := newSupervisor(l, d.manifest.Name, realLaunch(spec), l.superConfig())
	l.mu.Lock()
	l.sups = append(l.sups, sup)
	l.mu.Unlock()
	sup.start(ctx)
}

// superConfig derives the supervisor policy from Config; zero fields fall back to the spec
// defaults in newSupervisor.
func (l *Loader) superConfig() superConfig {
	return superConfig{
		maxAttempts:  l.cfg.MaxAttempts,
		drainTimeout: l.cfg.DrainTimeout,
		healthStable: l.cfg.HealthStable,
		log:          l.cfg.Log,
	}
}

// Stop drains and stops every launched plugin, bounded by ctx. main calls it AFTER the HTTP
// server has drained, so no in-flight request is left calling a plugin whose registry entry has
// been removed. The ctx is the SHARED shutdown budget covering both the HTTP drain and this — not
// a second independent timeout; if it expires, Stop returns and the remaining plugins are left to
// the OS (their pidfiles let the next boot sweep reclaim them).
func (l *Loader) Stop(ctx context.Context) {
	l.mu.Lock()
	cancel := l.bootCancel
	sups := append([]*supervisor(nil), l.sups...)
	l.mu.Unlock()

	if cancel != nil {
		cancel() // signal every supervisor to tear down (drain cancel-then-kill, then reap)
	}
	for _, s := range sups {
		select {
		case <-s.done:
		case <-ctx.Done():
			l.cfg.Log.Warn("plugin shutdown budget exhausted; remaining plugins left for the next boot sweep")
			return
		}
	}
}
