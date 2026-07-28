// Package scheduler owns the lifecycle of per-team challenge instances: spawn on
// demand (with quota + flag + TTL), extend, stop, expire on a TTL, and clean up
// at event end. It is in-process and tick-driven — not a job queue — and is the
// single writer of per-team lifecycle transitions. It talks to containers only
// through runtime.Manager and never imports the Docker SDK
// (docs/v0.2/04-scheduler.md).
package scheduler

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/osctf/platform/internal/apperr"
	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/flags"
	"github.com/osctf/platform/internal/metrics"
	"github.com/osctf/platform/internal/runtime"
)

// Config holds the tunable scheduler limits (from OSCTF_INSTANCE_* env).
type Config struct {
	TTL    time.Duration // default per-team TTL (0 = no TTL)
	Extend time.Duration // added per Extend
	MaxTTL time.Duration // max total lifetime
	Quota  int           // concurrent running per-team instances per team
}

// Scheduler drives per-team instance lifecycle.
type Scheduler struct {
	mgr   *runtime.Manager
	q     *gen.Queries
	ev    *events.Service
	flags *flags.Generator
	audit *audit.Logger
	clock clock.Clock
	log   *slog.Logger
	cfg   Config

	mu sync.Mutex // serializes spawn/expire; correctness itself lives in the DB
}

// New builds the scheduler.
func New(mgr *runtime.Manager, q *gen.Queries, ev *events.Service, flg *flags.Generator, auditLog *audit.Logger, clk clock.Clock, log *slog.Logger, cfg Config) *Scheduler {
	return &Scheduler{mgr: mgr, q: q, ev: ev, flags: flg, audit: auditLog, clock: clk, log: log, cfg: cfg}
}

// Start starts (or returns) the caller team's instance for a per_team challenge.
// created is true when a new instance was deployed, false when an existing running
// one was returned (idempotent).
func (s *Scheduler) Start(ctx context.Context, actorID, teamID, challengeID uuid.UUID) (inst runtime.Instance, created bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch, err := s.q.GetChallengeByID(ctx, challengeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return runtime.Instance{}, false, apperr.ErrNotFound
		}
		return runtime.Instance{}, false, err
	}
	if !ch.Visible {
		return runtime.Instance{}, false, apperr.ErrNotFound
	}
	if ch.Kind != "container" || ch.Instancing != "per_team" {
		return runtime.Instance{}, false, apperr.Conflictf("this challenge does not have per-team instances")
	}
	if err := s.requireRunning(ctx); err != nil {
		return runtime.Instance{}, false, err
	}

	// Idempotent: an already-running instance is returned as-is.
	if existing, ok, gerr := s.mgr.GetTeamInstance(ctx, challengeID, teamID); gerr != nil {
		return runtime.Instance{}, false, gerr
	} else if ok && existing.State == runtime.StateRunning {
		return existing, false, nil
	}

	// Quota: count the team's running per-team instances across all challenges.
	n, err := s.mgr.CountTeamRunning(ctx, teamID)
	if err != nil {
		return runtime.Instance{}, false, err
	}
	if n >= s.cfg.Quota {
		return runtime.Instance{}, false, apperr.Conflictf("your team is at its instance limit (%d/%d) — stop one to start another", n, s.cfg.Quota)
	}

	flag := ch.Flag
	if ch.FlagMode == "per_instance" {
		f, ferr := s.flags.New()
		if ferr != nil {
			return runtime.Instance{}, false, ferr
		}
		flag = f
	}

	inst, err = s.mgr.DeployForTeam(ctx, runtime.DeployReq{
		ChallengeID: challengeID, TeamID: teamID, Flag: flag, ExpiresAt: s.ttlFor(ch),
	})
	if err != nil {
		s.log.Warn("instance start: deploy failed",
			"challenge_id", challengeID, "team_id", teamID, "error", err.Error())
		return runtime.Instance{}, false, err
	}
	metrics.InstanceSpawns.Inc()
	s.audit.Log(ctx, actorID, "instance.spawn", "instance", inst.ID.String(), map[string]any{
		"challenge_id": challengeID.String(), "team_id": teamID.String(),
	})
	return inst, true, nil
}

// Stop stops and destroys the caller team's instance.
func (s *Scheduler) Stop(ctx context.Context, actorID, teamID, challengeID uuid.UUID) error {
	inst, ok, err := s.mgr.GetTeamInstance(ctx, challengeID, teamID)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrNotFound
	}
	if err := s.mgr.DestroyInstance(ctx, inst.ID); err != nil {
		return err
	}
	metrics.InstanceCleanups.WithLabelValues("stop").Inc()
	s.audit.Log(ctx, actorID, "instance.cleanup", "instance", inst.ID.String(), map[string]any{
		"challenge_id": challengeID.String(), "team_id": teamID.String(), "reason": "stop",
	})
	return nil
}

// Extend pushes the caller team's instance expiry forward by the configured step,
// capped at the maximum lifetime.
func (s *Scheduler) Extend(ctx context.Context, teamID, challengeID uuid.UUID) (runtime.Instance, error) {
	inst, ok, err := s.mgr.GetTeamInstance(ctx, challengeID, teamID)
	if err != nil {
		return runtime.Instance{}, err
	}
	if !ok {
		return runtime.Instance{}, apperr.ErrNotFound
	}
	if inst.ExpiresAt == nil {
		return inst, nil // no TTL to extend
	}
	now := s.clock()
	// Extend adds the step to the CURRENT expiry (not "now + step", which would
	// shorten a freshly-started instance), capped at the instance's max lifetime.
	maxLifetime := now.Add(s.cfg.MaxTTL)
	if inst.StartedAt != nil {
		maxLifetime = inst.StartedAt.Add(s.cfg.MaxTTL)
	}
	if !inst.ExpiresAt.Before(maxLifetime) {
		return runtime.Instance{}, apperr.Conflictf("this instance has reached its maximum lifetime")
	}
	next := inst.ExpiresAt.Add(s.cfg.Extend)
	if next.After(maxLifetime) {
		next = maxLifetime
	}
	row, err := s.q.SetInstanceExpiry(ctx, gen.SetInstanceExpiryParams{ID: inst.ID, ExpiresAt: &next})
	if err != nil {
		return runtime.Instance{}, err
	}
	inst.ExpiresAt = row.ExpiresAt
	return inst, nil
}

// RunExpiry drives the TTL-expiry ticker until ctx is cancelled.
func (s *Scheduler) RunExpiry(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := s.ExpireOnce(tctx); err != nil {
				s.log.Warn("instance expiry pass failed", "error", err.Error())
			}
			s.refreshGauge(tctx)
			cancel()
		}
	}
}

// ExpireOnce destroys every per-team instance past its TTL. Exported so tests can
// drive one pass deterministically with an injected clock.
func (s *Scheduler) ExpireOnce(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock()
	rows, err := s.q.ListExpiredInstances(ctx, &now)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if derr := s.mgr.DestroyInstance(ctx, row.ID); derr != nil {
			s.log.Warn("expire: destroy failed", "instance", row.ID, "error", derr.Error())
			continue
		}
		metrics.InstanceExpiries.Inc()
		s.audit.Log(ctx, uuid.Nil, "instance.expire", "instance", row.ID.String(), metaOf(row))
	}
	return nil
}

// CleanupEnded destroys all per-team instances (called on the running→ended edge).
// Shared instances are left running for a post-event practice window.
func (s *Scheduler) CleanupEnded(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.q.ListPerTeamInstances(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if derr := s.mgr.DestroyInstance(ctx, row.ID); derr != nil {
			s.log.Warn("cleanup: destroy failed", "instance", row.ID, "error", derr.Error())
			continue
		}
		metrics.InstanceCleanups.WithLabelValues("event-end").Inc()
		s.audit.Log(ctx, uuid.Nil, "instance.cleanup", "instance", row.ID.String(), metaOf(row, "event-end"))
	}
	return nil
}

// requireRunning enforces the running event phase.
func (s *Scheduler) requireRunning(ctx context.Context) error {
	e, err := s.ev.Get(ctx)
	if err != nil {
		return err
	}
	if s.ev.Phase(e) != events.PhaseRunning {
		return apperr.Conflictf("instances are available while the event is running")
	}
	return nil
}

// ttlFor resolves the instance expiry: per-challenge override wins; 0 = no TTL.
func (s *Scheduler) ttlFor(ch gen.Challenge) *time.Time {
	ttl := s.cfg.TTL
	if ch.InstanceTtlSeconds != nil {
		ttl = time.Duration(*ch.InstanceTtlSeconds) * time.Second
	}
	if ttl <= 0 {
		return nil
	}
	t := s.clock().Add(ttl)
	return &t
}

// refreshGauge tallies per-team instances by state into the gauge.
func (s *Scheduler) refreshGauge(ctx context.Context) {
	rows, err := s.q.ListPerTeamInstances(ctx)
	if err != nil {
		return
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.State]++
	}
	metrics.TeamInstances.Reset()
	for state, n := range counts {
		metrics.TeamInstances.WithLabelValues(state).Set(float64(n))
	}
}

func metaOf(row gen.Instance, reason ...string) map[string]any {
	m := map[string]any{"challenge_id": row.ChallengeID.String()}
	if row.TeamID != nil {
		m["team_id"] = row.TeamID.String()
	}
	if len(reason) > 0 {
		m["reason"] = reason[0]
	}
	return m
}
