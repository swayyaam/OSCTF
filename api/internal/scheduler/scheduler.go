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
	"sync/atomic"
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
	TTL       time.Duration // default per-team TTL (0 = no TTL)
	Extend    time.Duration // added per Extend
	MaxTTL    time.Duration // max total lifetime
	Quota     int           // concurrent running per-team instances per team
	ReapAfter time.Duration // reap pending/error rows (leaked ports) older than this
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

	// teamMu holds one lock per team with at least one in-flight holder or waiter.
	// A team's lock serializes ALL operations that touch that team's instances —
	// Start/Stop/Extend AND the per-row work of the sweeps (ExpireOnce, CleanupEnded,
	// ReapStaleOnce) — so its quota check is a correct read-modify-write and a sweep
	// can neither delete a row out from under an in-flight deploy nor destroy an
	// instance another op just extended. Other teams stay free, so one team's slow
	// (image-pull-length) deploy does not stall the platform. Each lock is a
	// capacity-1 channel (not sync.Mutex) so acquisition honours a caller's context,
	// and is refcounted so the map evicts a team's entry once idle (no unbounded
	// growth). Guarded by teamGuard.
	teamGuard sync.Mutex
	teamMu    map[uuid.UUID]*teamLock

	// expireOffset rotates the starting point of each expiry sweep so a fixed row
	// order cannot starve the same teams every pass (1e-iv).
	expireOffset atomic.Uint64
	// afterList, when set, is invoked by ExpireOnce right after it lists the expired
	// instances and before it destroys any — a test seam to inject a racing Extend
	// deterministically without reaching into the lock internals.
	afterList func()
}

// teamLock is a context-aware, refcounted mutex for one team. ch (capacity 1) is
// the mutex: a value in the buffer means held. refs counts holders + waiters so the
// registry knows when the entry is idle and can be evicted.
type teamLock struct {
	ch   chan struct{}
	refs int
}

// New builds the scheduler.
func New(mgr *runtime.Manager, q *gen.Queries, ev *events.Service, flg *flags.Generator, auditLog *audit.Logger, clk clock.Clock, log *slog.Logger, cfg Config) *Scheduler {
	return &Scheduler{mgr: mgr, q: q, ev: ev, flags: flg, audit: auditLog, clock: clk, log: log, cfg: cfg, teamMu: map[uuid.UUID]*teamLock{}}
}

// refTeamLock returns teamID's lock entry (creating it on first use) with its
// refcount already incremented, and a release func that drops the ref and evicts the
// entry when it reaches zero. The increment happens in the SAME critical section as
// the map read/create, so an entry can never be evicted between a goroutine's lookup
// and its use — two goroutines can never end up holding different structs for the
// same team (1e-iii).
func (s *Scheduler) refTeamLock(teamID uuid.UUID) (*teamLock, func()) {
	s.teamGuard.Lock()
	tl, ok := s.teamMu[teamID]
	if !ok {
		tl = &teamLock{ch: make(chan struct{}, 1)}
		s.teamMu[teamID] = tl
	}
	tl.refs++
	s.teamGuard.Unlock()
	return tl, func() {
		s.teamGuard.Lock()
		tl.refs--
		if tl.refs == 0 {
			delete(s.teamMu, teamID)
		}
		s.teamGuard.Unlock()
	}
}

// lockTeam acquires teamID's lock, honouring ctx while waiting. On success it
// returns an unlock func and true; if ctx is cancelled before the lock is acquired it
// returns a no-op func and false (the caller must not proceed).
func (s *Scheduler) lockTeam(ctx context.Context, teamID uuid.UUID) (func(), bool) {
	tl, release := s.refTeamLock(teamID)
	select {
	case tl.ch <- struct{}{}:
		return func() { <-tl.ch; release() }, true
	case <-ctx.Done():
		release()
		return func() {}, false
	}
}

// tryLockTeam acquires teamID's lock without blocking: if it is already held it
// returns false immediately. The periodic sweeps use this so a team that is mid-
// deploy is skipped instantly (retried next tick) instead of consuming the pass
// budget and starving every team behind it (1e-iv).
func (s *Scheduler) tryLockTeam(teamID uuid.UUID) (func(), bool) {
	tl, release := s.refTeamLock(teamID)
	select {
	case tl.ch <- struct{}{}:
		return func() { <-tl.ch; release() }, true
	default:
		release()
		return func() {}, false
	}
}

// Start starts (or returns) the caller team's instance for a per_team challenge.
// created is true when a new instance was deployed, false when an existing running
// one was returned (idempotent).
func (s *Scheduler) Start(ctx context.Context, actorID, teamID, challengeID uuid.UUID) (inst runtime.Instance, created bool, err error) {
	// Per-team lock only: serialize THIS team's Starts (so the quota read-modify-
	// write is correct and idempotent Start holds), without blocking other teams or
	// the deploy of another team. The slow rt.Deploy below runs under this lock, but
	// it is a single team's lock, not a global one.
	unlock, ok := s.lockTeam(ctx, teamID)
	if !ok {
		return runtime.Instance{}, false, ctx.Err()
	}
	defer unlock()

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
	unlock, locked := s.lockTeam(ctx, teamID) // serialize with this team's Start/Extend/sweeps
	if !locked {
		return ctx.Err()
	}
	defer unlock()
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
	unlock, locked := s.lockTeam(ctx, teamID) // serialize with this team's Start/Stop/sweeps
	if !locked {
		return runtime.Instance{}, ctx.Err()
	}
	defer unlock()
	inst, ok, err := s.mgr.GetTeamInstance(ctx, challengeID, teamID)
	if err != nil {
		return runtime.Instance{}, err
	}
	if !ok {
		return runtime.Instance{}, apperr.ErrNotFound
	}
	// Extend is a running-phase operation (3a-ix). Instance TTL is NOT capped at the
	// event end — CleanupEnded reclaims per-team instances when the event ends — so
	// without this gate a participant could push a still-live instance's expiry past the
	// end during the cleanup window; if the one-shot cleanup already swept their team, the
	// instance would then outlive the event, reclaimed only by the (now later) TTL reaper.
	// Gating here keeps phases:[running] enforced rather than emergent. Placed AFTER the
	// existence check so a missing instance stays 404, not 409.
	if err := s.requireRunning(ctx); err != nil {
		return runtime.Instance{}, err
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
			if ctx.Err() != nil {
				return // shutting down: don't start a new pass
			}
			// Background-derived, not ctx-derived: a DestroyInstance already in flight
			// (expiry or reap) completes even once shutdown is signalled. The loop
			// stops via the ctx.Err check above; main joins this goroutine (bounded).
			tctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := s.ExpireOnce(tctx); err != nil {
				s.log.Warn("instance expiry pass failed", "error", err.Error())
			}
			if _, err := s.ReapStaleOnce(tctx); err != nil {
				s.log.Warn("instance reap pass failed", "error", err.Error())
			}
			s.refreshGauge(tctx)
			cancel()
		}
	}
}

// destroyLocked destroys instanceID under teamID's lock, re-reading the row first
// and destroying only if it still exists and eligible (if non-nil) reports it should
// go. Taking the team lock makes the destroy mutually exclusive with that team's
// Start/Stop/Extend, so a sweep cannot delete a row out from under an in-flight
// deploy (no orphan container) nor race a concurrent op; re-reading and re-checking
// defends against a state change (notably an Extend) between listing and destroying.
//
// wait selects how the lock is taken: the periodic sweeps (expiry/reap) pass
// wait=false so a busy team is skipped instantly (skippedBusy=true, retried next
// tick); event-end cleanup passes wait=true so it blocks — bounded by ctx — for an
// in-flight deploy to finish before tearing the completed instance down. A nil teamID
// (shared instance) is destroyed without a team lock — no per-team op contends there.
func (s *Scheduler) destroyLocked(ctx context.Context, teamID *uuid.UUID, instanceID uuid.UUID, wait bool, eligible func(gen.Instance) bool) (destroyed, skippedBusy bool) {
	if teamID != nil {
		var unlock func()
		var ok bool
		if wait {
			unlock, ok = s.lockTeam(ctx, *teamID)
		} else {
			unlock, ok = s.tryLockTeam(*teamID)
		}
		if !ok {
			return false, true
		}
		defer unlock()
	}
	cur, err := s.q.GetInstanceByID(ctx, instanceID)
	if err != nil {
		return false, false // already gone (or unreadable): nothing to do
	}
	if eligible != nil && !eligible(cur) {
		return false, false // condition no longer holds (e.g. extended out of expiry)
	}
	if derr := s.mgr.DestroyInstance(ctx, instanceID); derr != nil {
		s.log.Warn("destroy failed", "instance", instanceID, "error", derr.Error())
		return false, false
	}
	return true, false
}

// ExpireOnce destroys every per-team instance past its TTL. A team that is busy
// (mid-deploy) is skipped without blocking, and its expired row — still expired —
// is picked up on a later pass. The start of the row list is rotated each pass so no
// fixed order can starve the same teams. Exported so tests can drive one pass
// deterministically with an injected clock.
func (s *Scheduler) ExpireOnce(ctx context.Context) error {
	now := s.clock()
	rows, err := s.q.ListExpiredInstances(ctx, &now)
	if err != nil {
		return err
	}
	if s.afterList != nil {
		s.afterList()
	}
	for _, row := range rotate(rows, s.expireOffset.Add(1)) {
		// wait=false: skip a mid-deploy team instantly (retried next pass). Re-verify
		// under the lock that the instance is still expired — an Extend may have pushed
		// its expiry into the future after we listed it.
		destroyed, _ := s.destroyLocked(ctx, row.TeamID, row.ID, false, func(cur gen.Instance) bool {
			return cur.ExpiresAt != nil && cur.ExpiresAt.Before(now)
		})
		if destroyed {
			metrics.InstanceExpiries.Inc()
			s.audit.Log(ctx, uuid.Nil, "instance.expire", "instance", row.ID.String(), metaOf(row))
		}
	}
	metrics.MarkSuccess(metrics.ExpiryLastSuccess) // liveness heartbeat (even on a no-op pass)
	return nil
}

// rotate returns items reordered to start at offset%len (round-robin fairness),
// leaving the original slice untouched. A zero-length slice is returned as-is.
func rotate[T any](items []T, offset uint64) []T {
	n := len(items)
	if n == 0 {
		return items
	}
	start := int(offset % uint64(n)) //nolint:gosec // G115: result is < n (an int), always fits
	out := make([]T, 0, n)
	out = append(out, items[start:]...)
	out = append(out, items[:start]...)
	return out
}

// CleanupEnded destroys all per-team instances (called while the event is ended).
// Shared instances are left running for a post-event practice window. Each destroy
// takes the team lock (waiting, bounded by ctx) so a cleanup landing mid-deploy tears
// the completed instance down cleanly rather than orphaning the container it was
// about to create. Teams are processed concurrently so many in-flight deploys are
// awaited in parallel, not summed. Returns the number of per-team instances still
// present after the pass: 0 means converged; a positive count means some teams were
// still busy and the caller should run another pass (idempotent) until it reaches 0.
func (s *Scheduler) CleanupEnded(ctx context.Context) (int, error) {
	rows, err := s.q.ListPerTeamInstances(ctx)
	if err != nil {
		return 0, err
	}
	const maxConcurrent = 16
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for _, row := range rows {
		wg.Add(1)
		sem <- struct{}{}
		go func(row gen.Instance) {
			defer wg.Done()
			defer func() { <-sem }()
			destroyed, _ := s.destroyLocked(ctx, row.TeamID, row.ID, true, nil)
			if destroyed {
				metrics.InstanceCleanups.WithLabelValues("event-end").Inc()
				s.audit.Log(ctx, uuid.Nil, "instance.cleanup", "instance", row.ID.String(), metaOf(row, "event-end"))
			}
		}(row)
	}
	wg.Wait()

	// Definitive remaining count for the caller's convergence check. Uses a fresh,
	// bounded context so it reports the truth even if ctx expired mid-pass.
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	after, err := s.q.ListPerTeamInstances(cctx)
	if err != nil {
		return len(rows), err
	}
	return len(after), nil
}

// ReapStaleOnce destroys instances stuck in pending/error past the configured
// ReapAfter threshold, reclaiming the host_port each still holds. allocateRow
// reserves a port before Deploy; a failed or interrupted Deploy leaves that row —
// and its port — behind, and nothing else sweeps pending/error rows (ExpireOnce
// skips them, being TTL-driven). Returns the number reaped. Exported so tests can
// drive one pass deterministically; a ReapAfter of 0 disables reaping. A busy team
// is skipped without blocking (retried next pass) and each destroy re-checks the row
// is still stale, so it never races a concurrent Start/Stop for that team.
func (s *Scheduler) ReapStaleOnce(ctx context.Context) (int, error) {
	if s.cfg.ReapAfter <= 0 {
		return 0, nil
	}
	rows, err := s.mgr.ListStale(ctx, s.cfg.ReapAfter)
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, row := range rows {
		destroyed, _ := s.destroyLocked(ctx, row.TeamID, row.ID, false, func(cur gen.Instance) bool {
			return cur.State == string(runtime.StatePending) || cur.State == string(runtime.StateError)
		})
		if !destroyed {
			continue
		}
		meta := map[string]any{"challenge_id": row.ChallengeID.String(), "reason": "reap-stale", "state": string(row.State)}
		if row.TeamID != nil {
			meta["team_id"] = row.TeamID.String()
		}
		metrics.InstanceCleanups.WithLabelValues("reap").Inc()
		s.audit.Log(ctx, uuid.Nil, "instance.cleanup", "instance", row.ID.String(), meta)
		reaped++
	}
	metrics.MarkSuccess(metrics.ReapLastSuccess) // liveness heartbeat, after the pass completes
	return reaped, nil
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
