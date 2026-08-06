// Command platform is the OSCTF server binary. Subcommands: serve | migrate | seed.
// This file is the composition root: the only place concrete implementations are
// wired to interfaces.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/config"
	"github.com/osctf/platform/internal/db"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/flags"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/httpx"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/scheduler"
	"github.com/osctf/platform/internal/scoreboard"
	"github.com/osctf/platform/internal/seed"
	"github.com/osctf/platform/internal/storage"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/users"
	appversion "github.com/osctf/platform/internal/version"
	"github.com/osctf/platform/internal/ws"
)

// version is set via -ldflags at build time.
var version = "dev"

func main() {
	appversion.Version = version

	args := os.Args[1:]
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "serve":
		run(cmdServe)
	case "migrate":
		run(cmdMigrate)
	case "seed":
		run(cmdSeed)
	case "version", "-v", "--version":
		fmt.Println("platform", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`platform — OSCTF server

Usage:
  platform serve     Run the HTTP server (migrates + seeds on first boot)
  platform migrate   Run database migrations and exit
  platform seed      Seed the admin, default event, and example challenges
  platform version   Print the build version`)
}

// run loads config, builds the logger, and invokes fn; it centralizes fatal exits.
func run(fn func(context.Context, *config.Config, *slog.Logger) error) {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	log := newLogger(cfg)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err = fn(ctx, cfg, log)
	stop()
	if err != nil {
		log.Error("fatal", "error", err.Error())
		os.Exit(1)
	}
}

func newLogger(cfg *config.Config) *slog.Logger {
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

func cmdMigrate(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	log.Info("running migrations")
	if err := db.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	log.Info("migrations complete")
	return nil
}

func cmdSeed(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	pool, err := db.Connect(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := storage.NewS3Store(ctx, storage.Config{
		Endpoint: cfg.S3Endpoint, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		Bucket: cfg.S3Bucket, UseSSL: cfg.S3UseSSL,
	})
	if err != nil {
		return err
	}
	q := gen.New(pool)
	if err := seed.EnsureAdmin(ctx, q, cfg, log); err != nil {
		return err
	}
	if err := events.New(q, clock.System()).EnsureDefault(ctx); err != nil {
		return err
	}
	if cfg.SeedExamples {
		if err := seed.NewExampleSeeder(q, challenges.New(q, store), log).Seed(ctx, cfg.ExamplesDir); err != nil {
			return err
		}
	}
	log.Info("seed complete")
	return nil
}

func cmdServe(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	log.Info("starting platform", "version", version, "addr", cfg.HTTPAddr)

	pool, err := db.Connect(ctx, cfg.DatabaseURL, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := db.Migrate(ctx, cfg.DatabaseURL); err != nil {
		return err
	}
	log.Info("database ready")

	rdb, err := redisx.Connect(ctx, cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()
	log.Info("redis ready")

	store, err := storage.NewS3Store(ctx, storage.Config{
		Endpoint: cfg.S3Endpoint, AccessKey: cfg.S3AccessKey, SecretKey: cfg.S3SecretKey,
		Bucket: cfg.S3Bucket, UseSSL: cfg.S3UseSSL,
	})
	if err != nil {
		return err
	}
	log.Info("object storage ready", "bucket", cfg.S3Bucket)

	q := gen.New(pool)
	clk := clock.System()

	if err := seed.EnsureAdmin(ctx, q, cfg, log); err != nil {
		return err
	}
	eventsSvc := events.New(q, clk)
	if err := eventsSvc.EnsureDefault(ctx); err != nil {
		return err
	}

	ready := func(ctx context.Context) map[string]string {
		failing := map[string]string{}
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := pool.Ping(pingCtx); err != nil {
			failing["postgres"] = err.Error()
		}
		if err := rdb.Ping(pingCtx).Err(); err != nil {
			failing["redis"] = err.Error()
		}
		if err := store.Ready(pingCtx); err != nil {
			failing["minio"] = err.Error()
		}
		return failing
	}

	// Composition root: concrete implementations wired to interfaces here only.
	// Bound concurrent argon2id hashing before anything can hash (seeding included)
	// so a registration/login burst can't OOM the host (issue #3).
	hashConc := cfg.PasswordHashConcurrency
	if hashConc <= 0 {
		hashConc = auth.DefaultHashConcurrency()
	}
	auth.ConfigureHashGate(hashConc, cfg.PasswordHashMaxWait)
	log.Info("argon2id hashing gate configured",
		"concurrency", hashConc, "max_wait", cfg.PasswordHashMaxWait, "peak_mem_mb", hashConc*64)

	sessions := auth.NewSessionStore(rdb, cfg.SessionTTL)
	usersSvc := users.New(q, sessions, cfg.RegistrationOpen)
	teamsSvc := teams.New(pool, cfg.TeamMaxSize)
	// Self-heal any team a pre-fix build left with a captain who is no longer a member
	// (a concurrent-leave race, since fixed). Runs before serving; a no-op on a healthy
	// database. A stranded team has no in-product recovery, so repair it loudly.
	if repaired, err := teamsSvc.RepairStrandedCaptains(ctx); err != nil {
		return err
	} else if len(repaired) > 0 {
		for _, r := range repaired {
			log.Warn("reassigned captaincy for a team whose captain was not a member (data repaired)",
				"team_id", r.TeamID, "new_captain", r.NewCaptain)
		}
		log.Warn("repaired stranded team captaincies on startup", "count", len(repaired))
	}
	challengesSvc := challenges.New(q, store)
	if cfg.SeedExamples {
		if err := seed.NewExampleSeeder(q, challengesSvc, log).Seed(ctx, cfg.ExamplesDir); err != nil {
			return err
		}
	}
	auditLog := audit.New(q, log)
	submissionsSvc := submissions.New(pool, eventsSvc, clk, auditLog)
	scoreboardSvc := scoreboard.New(q, rdb, eventsSvc, clk)

	// bgWG joins the long-lived background workers so shutdown waits for any
	// in-flight pass (notably a DestroyInstance mid-Docker-call) to finish rather
	// than the process exiting out from under it. Each worker also runs its passes
	// under Background-derived timeouts (not the signal ctx), so the current pass
	// completes even as the loop is told to stop.
	var bgWG sync.WaitGroup

	hub := ws.NewHub(log)
	// A live WS connection is a socket fd; a global cap above RLIMIT_NOFILE would hit
	// "accept: too many open files" — taking down HTTP, Postgres, and Docker together —
	// before admission control sheds WS load. Clamp the cap to the fd budget and warn.
	wsMaxConns := cfg.WSMaxConns
	var rlim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlim); err != nil {
		log.Warn("could not read RLIMIT_NOFILE; leaving the WebSocket connection cap unchecked", "error", err.Error())
	} else if eff, clamped := ws.SafeGlobalCap(cfg.WSMaxConns, uint64(rlim.Cur)); clamped {
		log.Warn("OSCTF_WS_MAX_CONNS exceeds the file-descriptor headroom — clamping to avoid fd exhaustion. Raise the process ulimit (RLIMIT_NOFILE) for large events; see docs/v0.1/10-deployment.md.",
			"configured", cfg.WSMaxConns, "effective", eff, "rlimit_nofile_soft", rlim.Cur)
		wsMaxConns = eff
	}
	hub.SetLimits(ws.Limits{
		MaxConns:        wsMaxConns,
		MaxConnsPerKey:  cfg.WSMaxConnsPerConn,
		HandshakeBurst:  cfg.WSHandshakeBurst,
		HandshakeWindow: cfg.WSHandshakeWindow,
	})
	// Key admission on the authenticated user where a session exists (so a shared NAT of
	// logged-in players is not squeezed through one IP budget), falling back to the
	// proxy-aware client IP for anonymous connections.
	hub.SetKeyResolver(func(r *http.Request) string {
		if id, ok := auth.IdentityFrom(r.Context()); ok {
			return "u:" + id.UserID.String()
		}
		return "ip:" + httpx.ClientIP(r, cfg.TrustProxy)
	})
	bgWG.Add(1)
	go func() { defer bgWG.Done(); hub.Run(ctx) }()
	scoreboardSvc.SetBroadcaster(func(s scoreboard.Snapshot) { hub.BroadcastScoreboard(handlers.ToScoreboard(s)) })

	dockerRT, err := runtime.NewDockerRuntime(q, log, cfg.DockerHost)
	if err != nil {
		return err
	}
	rtMgr := runtime.NewManager(dockerRT, q, cfg.PublicHost, cfg.PortRangeStart, cfg.PortRangeEnd)
	if rerr := rtMgr.Reconcile(ctx); rerr != nil {
		log.Warn("initial runtime reconcile failed", "error", rerr.Error())
	} else {
		log.Info("runtime reconciled")
	}
	// Self-check cross-network isolation (async, best-effort). Native Linux Docker
	// isolates per-team instances; Docker Desktop's VM does not once a host port is
	// published (which every instance does), so warn loudly there.
	go func() {
		isolated, ierr := rtMgr.VerifyIsolation(ctx)
		switch {
		case ierr != nil:
			log.Debug("network isolation self-check skipped", "error", ierr.Error())
		case !isolated:
			log.Warn("SECURITY: this Docker daemon does NOT enforce network isolation between " +
				"per-team challenge instances — one team can reach another team's container. This is " +
				"expected on Docker Desktop; run per-team challenges on native Linux Docker for real " +
				"isolation (docs/v0.2/03-runtime.md).")
		default:
			log.Info("network isolation self-check passed: per-team instances are isolated")
		}
	}()
	sched := scheduler.New(rtMgr, q, eventsSvc, flags.NewGenerator(cfg.FlagPrefix), auditLog, clk, log, scheduler.Config{
		TTL: cfg.InstanceTTL, Extend: cfg.InstanceExtend, MaxTTL: cfg.InstanceMaxTTL, Quota: cfg.TeamInstanceQuota,
		ReapAfter: cfg.InstanceReapAfter,
	})
	provider := auth.NewEmailPasswordProvider(q, func(ctx context.Context, id uuid.UUID, newHash string) {
		if err := usersSvc.RehashPassword(ctx, id, newHash); err != nil {
			log.Warn("password rehash failed", "user_id", id, "error", err.Error())
		}
	})
	limiter := redisx.NewLimiter(rdb)

	authRegistry := auth.NewRegistry(provider)
	// Refuse to boot with no way to log in: email login disabled and no other provider
	// registered. Booting a login-less deployment is worse than failing loudly here.
	if !authRegistry.HasUsableLogin(cfg.AuthEmailLogin) {
		return fmt.Errorf("no login method available: OSCTF_AUTH_EMAIL_LOGIN=false and no auth provider is registered — enable email login or register a provider")
	}

	h := handlers.New(handlers.Deps{
		Users:       usersSvc,
		Teams:       teamsSvc,
		Events:      eventsSvc,
		Challenges:  challengesSvc,
		Submissions: submissionsSvc,
		Scoreboard:  scoreboardSvc,
		Runtime:     rtMgr,
		Scheduler:   sched,
		Recompute: func(rctx context.Context) {
			if err := scoreboardSvc.Recompute(rctx); err != nil {
				log.Warn("scoreboard recompute failed", "error", err.Error())
			}
		},
		Auth:               authRegistry,
		EmailLoginDisabled: !cfg.AuthEmailLogin,
		Sessions:           sessions,
		Limiter:            limiter,
		Audit:              auditLog,
		Log:                log,
		RegisterIPBurst:    cfg.RegisterIPBurst,
		RegisterIPWindow:   cfg.RegisterIPWindow,
		LoginIPBurst:       cfg.LoginIPBurst,
		LoginIPWindow:      cfg.LoginIPWindow,
		SecureCookies:      cfg.IsHTTPS(),
		TrustProxy:         cfg.TrustProxy,
		SessionTTL:         cfg.SessionTTL,
		MaxAttachmentMB:    cfg.MaxAttachmentMB,
	})

	if err := scoreboardSvc.Recompute(ctx); err != nil {
		log.Warn("initial scoreboard compute failed", "error", err.Error())
	}

	handler := httpserver.New(httpserver.Deps{
		Log:           log,
		Handlers:      h,
		Ready:         ready,
		Sessions:      sessions,
		BaseOrigin:    cfg.BaseOrigin(),
		CORSDevOrigin: cfg.CORSDevOrigin,
		WSHandler:     hub.Handler(),
		TrustProxy:    cfg.TrustProxy,
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	bgWG.Add(3)
	go func() { defer bgWG.Done(); runTickers(ctx, log, eventsSvc, scoreboardSvc, hub, sched) }()
	go func() { defer bgWG.Done(); runReconcile(ctx, log, rtMgr) }()
	go func() { defer bgWG.Done(); sched.RunExpiry(ctx) }()

	serveErr := make(chan error, 1)
	go func() {
		log.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received, draining")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		// Wait for the background workers (ws hub, tickers, reconcile, expiry/reap) to
		// return. They observe ctx and stop after finishing any in-flight pass; because
		// those passes run under Background-derived timeouts, a DestroyInstance already
		// underway completes instead of being cut off mid-Docker-call. Bounded so a
		// wedged worker cannot hang shutdown indefinitely.
		if !waitBounded(&bgWG, 10*time.Second) {
			log.Warn("background workers did not drain within 10s; exiting anyway")
		}
		log.Info("shutdown complete")
		return nil
	}
}

// waitBounded waits for wg, returning true if it finished within d and false on
// timeout. The spawned waiter goroutine outlives a timeout return but ends when wg
// eventually completes; it never leaks past process exit.
func waitBounded(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// runTickers drives periodic background work: freeze snapshot capture and event
// phase-transition broadcasts. Interval 15s (docs/v0.1/01-architecture.md).
func runTickers(ctx context.Context, log *slog.Logger, ev *events.Service, sb *scoreboard.Service, hub *ws.Hub, sched *scheduler.Scheduler) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	lastPhase := ""
	endCleanupDone := false // latched true once the event-end sweep fully converges
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return // shutting down: don't start a new pass
			}
			// Background-derived, not ctx-derived: an in-flight pass (esp. the
			// event-end CleanupEnded below, which destroys containers) finishes even
			// once shutdown is signalled; the loop stops via the ctx.Err check above.
			tctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := sb.MaybeSnapshotFreeze(tctx); err != nil {
				log.Warn("freeze snapshot failed", "error", err.Error())
			}
			if e, err := ev.Get(tctx); err == nil {
				phase := string(ev.Phase(e))
				if lastPhase != "" && phase != lastPhase {
					hub.BroadcastPhase(phase)
					if rerr := sb.Recompute(tctx); rerr != nil {
						log.Warn("recompute on phase change failed", "error", rerr.Error())
					}
				}
				lastPhase = phase

				// Event-end teardown runs every tick while ended until it converges,
				// not only on the transition edge: each CleanupEnded destroy waits
				// (bounded) for that team's lock, so an instance still mid-deploy is
				// torn down cleanly once the deploy finishes. If a pass leaves some team
				// still busy past its budget, the latch stays false and the next tick
				// retries until no per-team instances remain.
				if phase == string(events.PhaseEnded) {
					if !endCleanupDone {
						cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Minute)
						remaining, cerr := sched.CleanupEnded(cctx)
						ccancel()
						switch {
						case cerr != nil:
							log.Warn("event-end instance cleanup failed", "error", cerr.Error())
						case remaining == 0:
							endCleanupDone = true
						default:
							log.Warn("event-end instance cleanup incomplete; retrying next tick", "remaining", remaining)
						}
					}
				} else {
					endCleanupDone = false // event no longer ended (e.g. re-opened): re-arm
				}
			}
			cancel()
		}
	}
}

// runReconcile periodically aligns tracked instances with actual container state
// (docs/v0.1/08-challenge-runtime.md). Interval 60s.
func runReconcile(ctx context.Context, log *slog.Logger, rt *runtime.Manager) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if ctx.Err() != nil {
				return // shutting down: don't start a new pass
			}
			// Background-derived so an in-flight reconcile (which may be removing a
			// container/network) is not cut off at shutdown; the loop stops above.
			rctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := rt.Reconcile(rctx); err != nil {
				log.Warn("runtime reconcile failed", "error", err.Error())
			}
			cancel()
		}
	}
}
