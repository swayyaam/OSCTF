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
	sessions := auth.NewSessionStore(rdb, cfg.SessionTTL)
	usersSvc := users.New(q, sessions, cfg.RegistrationOpen)
	teamsSvc := teams.New(pool, cfg.TeamMaxSize)
	challengesSvc := challenges.New(q, store)
	if cfg.SeedExamples {
		if err := seed.NewExampleSeeder(q, challengesSvc, log).Seed(ctx, cfg.ExamplesDir); err != nil {
			return err
		}
	}
	auditLog := audit.New(q, log)
	submissionsSvc := submissions.New(pool, eventsSvc, clk, auditLog)
	scoreboardSvc := scoreboard.New(q, rdb, eventsSvc, clk)

	hub := ws.NewHub(log)
	go hub.Run(ctx)
	scoreboardSvc.SetBroadcaster(hub.BroadcastScoreboard)

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
	sched := scheduler.New(rtMgr, q, eventsSvc, flags.NewGenerator(cfg.FlagPrefix), auditLog, clk, log, scheduler.Config{
		TTL: cfg.InstanceTTL, Extend: cfg.InstanceExtend, MaxTTL: cfg.InstanceMaxTTL, Quota: cfg.TeamInstanceQuota,
	})
	provider := auth.NewEmailPasswordProvider(q, func(ctx context.Context, id uuid.UUID, newHash string) {
		if err := usersSvc.RehashPassword(ctx, id, newHash); err != nil {
			log.Warn("password rehash failed", "user_id", id, "error", err.Error())
		}
	})
	limiter := redisx.NewLimiter(rdb)

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
		Auth:            provider,
		Sessions:        sessions,
		Limiter:         limiter,
		Audit:           auditLog,
		SecureCookies:   cfg.IsHTTPS(),
		TrustProxy:      cfg.TrustProxy,
		SessionTTL:      cfg.SessionTTL,
		MaxAttachmentMB: cfg.MaxAttachmentMB,
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
	})

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go runTickers(ctx, log, eventsSvc, scoreboardSvc, hub, sched)
	go runReconcile(ctx, log, rtMgr)
	go sched.RunExpiry(ctx)

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
		log.Info("shutdown complete")
		return nil
	}
}

// runTickers drives periodic background work: freeze snapshot capture and event
// phase-transition broadcasts. Interval 15s (docs/v0.1/01-architecture.md).
func runTickers(ctx context.Context, log *slog.Logger, ev *events.Service, sb *scoreboard.Service, hub *ws.Hub, sched *scheduler.Scheduler) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	lastPhase := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
					// Event just ended: tear down every per-team instance.
					if phase == string(events.PhaseEnded) {
						if cerr := sched.CleanupEnded(tctx); cerr != nil {
							log.Warn("event-end instance cleanup failed", "error", cerr.Error())
						}
					}
				}
				lastPhase = phase
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
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := rt.Reconcile(rctx); err != nil {
				log.Warn("runtime reconcile failed", "error", err.Error())
			}
			cancel()
		}
	}
}
