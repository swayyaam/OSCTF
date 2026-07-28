# 04 — The Scheduler

The scheduler is the one genuinely new subsystem in v0.2. It owns per-team instance
**lifecycle**: spawn on demand, enforce quota, expire on TTL, extend, and clean up at
event end. It is in-process and tick-driven — the same pattern as v0.1's freeze/phase
ticker ([`main.go:321`](../../api/cmd/platform/main.go#L321)) and reconcile ticker
([`main.go:352`](../../api/cmd/platform/main.go#L352)) — **not** a job queue.

Package: `api/internal/scheduler`. Imports `runtime` (Manager), `challenges`, `events`,
`db/gen`, `clock`, `flags`, `audit`, `metrics`. **Never** imports the Docker SDK.

## Responsibilities (single writer of per-team lifecycle)

```go
type Scheduler struct {
    mgr    *runtime.Manager
    q      *gen.Queries
    ev     *events.Service
    flags  *flags.Generator      // per-instance flag gen (05-flags.md)
    audit  *audit.Recorder
    clock  clock.Clock           // injected — expiry tests use a fake clock
    log    *slog.Logger
    cfg    Config                // TTL, extend, maxTTL, quota
    mu     sync.Mutex            // serializes spawn/expire (correctness is in the DB)
}

type Config struct {
    TTL      time.Duration // OSCTF_INSTANCE_TTL       (default 3600s)
    Extend   time.Duration // OSCTF_INSTANCE_EXTEND    (default 1800s)
    MaxTTL   time.Duration // OSCTF_INSTANCE_MAX_TTL   (default 14400s)
    Quota    int           // OSCTF_TEAM_INSTANCE_QUOTA(default 3)
}
```

Public methods (called by handlers):

```go
func (s *Scheduler) Start(ctx, teamID, challengeID uuid.UUID) (runtime.Instance, error)
func (s *Scheduler) Stop(ctx, teamID, challengeID uuid.UUID) error
func (s *Scheduler) Extend(ctx, teamID, challengeID uuid.UUID) (time.Time, error)
```

Background loops (started in `serve`, cancelled on shutdown):

```go
func (s *Scheduler) RunExpiry(ctx)   // 30s ticker
func (s *Scheduler) RunCleanup(ctx)  // folded into the 15s phase ticker (or its own 15s)
```

## Start (spawn on demand)

```
Start(team, challenge):
  s.mu.Lock(); defer Unlock()                      // avoid duplicate concurrent spawns
  ch  = challenges.Get(challenge)                   // must be visible, container, instancing=per_team
  ev  = events.Get();  require Phase == running     // else 409 event-not-running
  if existing := mgr.GetTeamInstance(team, challenge); existing.running:
      return existing                               // idempotent
  n = mgr.CountTeamRunning(team)
  if n >= cfg.Quota: return 409 quota-exceeded
  flag = (ch.flag_mode==per_instance) ? flags.New(challenge, team) : ch.flag
  expiresAt = ttlFor(ch)                            // now + (ch.instance_ttl ?? cfg.TTL); nil if 0
  inst = mgr.DeployForTeam({challenge, team, flag, expiresAt})   // creates row, deploys
  audit.Record("instance.spawn", team, {challenge, instance})    // NEVER the flag
  metrics.instanceSpawns.Inc()
  return inst
```

Notes:

- **Quota is per team, across all challenges**, counting `state='running'` per-team rows
  (`CountTeamRunningInstances`). A `pending` deploy in flight holds the mutex, so two
  simultaneous Starts can't both slip past the quota.
- Deploy is **synchronous** (v0.1's 120 s image-pull cap). The handler request blocks;
  the frontend shows a spinner. No queue, no async job. If the image is already built
  (examples are), it is fast.
- `ttlFor`: per-challenge `instance_ttl_seconds` overrides `cfg.TTL`; `0` → `expires_at`
  nil (event-end cleanup only); otherwise `now + ttl`.
- On deploy failure the row is left `error` by the runtime (v0.1 behaviour); Start returns
  the errored instance and the UI shows the error + a Retry (which re-Starts).

## Extend

```
Extend(team, challenge):
  inst = mgr.GetTeamInstance(team, challenge); require running (else 404 no-instance)
  cap  = inst.started_at + cfg.MaxTTL
  next = min(now + cfg.Extend, cap)
  if inst.expires_at != nil && inst.expires_at >= cap: return 409 max-lifetime-reached
  SetInstanceExpiry(inst.id, next)
  return next
```

Extending an instance with no TTL (`expires_at IS NULL`, from `instance_ttl_seconds=0`) is
a no-op success (there is nothing to extend); the UI hides Extend when there is no
countdown.

## Stop

```
Stop(team, challenge):
  inst = mgr.GetTeamInstance(team, challenge); require exists (else 404)
  require inst.team_id == team                     // a team can only stop its own
  mgr.DestroyInstance(inst.id)                     // remove container + delete row (frees port+quota)
  audit.Record("instance.cleanup", team, {challenge, instance, reason:"stop"})
```

Admins stop **any** instance through a separate admin path
([`06-api.md`](06-api.md)) that skips the ownership check.

## Expiry ticker (30 s)

```
RunExpiry:
  every 30s (ctx-scoped, 30s op timeout):
    s.mu.Lock()
    rows = ListExpiredInstances(now = clock.Now())   // expires_at < now, not already terminal
    for row in rows:
        mgr.DestroyInstance(row.id)
        audit.Record("instance.expire", owner=row.team_id, {challenge, instance})
        metrics.instanceExpiries.Inc()
    s.mu.Unlock()
```

- The 30 s interval bounds worst-case over-run to 30 s past `expires_at` — fine for CTF
  TTLs measured in hours.
- **Testability:** `clock` is injected. The expiry unit/integration test sets a fake clock,
  creates an instance with `expires_at = t0+1h`, advances the clock, calls one expiry pass
  directly (not the ticker), and asserts the container is destroyed and the row gone. No
  `time.Sleep`, deterministic (mirrors the v0.1 scoreboard freeze test approach).

## Event-end cleanup

Folded into the existing 15 s phase ticker
([`main.go:335`](../../api/cmd/platform/main.go#L335)): when the phase transitions
`running → ended`, destroy **all** per-team instances.

```
on phase running->ended:
  rows = ListPerTeamInstances()          // team_id IS NOT NULL, any state
  for row in rows: mgr.DestroyInstance(row.id); audit "instance.cleanup" reason:"event-end"
```

**Shared instances are left running** — organizers commonly keep them for a post-event
practice window; they are torn down by the operator (or `docker compose down`), as in v0.1.
Cleanup is idempotent: a second `ended` tick finds no per-team rows and does nothing.

## Concurrency & correctness

- **Correctness lives in the DB.** The partial unique indexes
  (`uq_instances_per_team`, `uq_instances_shared`) and the `allocate` retry-on-`23505`
  loop guarantee at most one instance per (challenge, team) and unique ports even without
  the mutex. The `sync.Mutex` only prevents *wasted* concurrent deploys and makes quota
  checks read-then-write atomic within one process. (Single-process deployment in v0.2, as
  v0.1 — horizontal scale is v0.4, and would swap the mutex for a Redis/advisory lock.)
- **Ordering:** Start acquires the mutex for the whole quota-check→deploy window, so a team
  spamming Start on two challenges serializes and cannot exceed quota. Expiry takes the
  same mutex so it never races a Start on the same team's slot count.
- **Shutdown:** both loops are `ctx`-cancelled; in-flight `DestroyInstance` finishes under
  a `context.WithoutCancel` timeout so a shutdown mid-expiry doesn't leak a container.

## Wiring in `serve` (main.go)

```go
sched := scheduler.New(rtMgr, q, eventsSvc, flagGen, auditRec, clk, log, schedCfg)
handlers.Deps.Scheduler = sched
go sched.RunExpiry(ctx)
// event-end cleanup: pass sched into runTickers so the running->ended edge calls
// sched.CleanupEnded(tctx) alongside the existing BroadcastPhase/Recompute.
```

## Metrics

Add to the `metrics` package (Prometheus, scraped at `/metrics` as in v0.1):

| Metric | Type | Meaning |
|---|---|---|
| `osctf_instance_spawns_total` | counter | per-team Start deploys |
| `osctf_instance_expiries_total` | counter | TTL expirations |
| `osctf_instance_cleanups_total{reason}` | counter | stop / event-end |
| `osctf_team_instances{state}` | gauge | running per-team instances (from `CountInstancesByState`, team-owned) |
| `osctf_flag_sharing_signals_total` | counter | see [`05-flags.md`](05-flags.md) |

The v0.1 `osctf_instances{state}` gauge stays (now spans both owner kinds).

## Failure modes (explicit)

| Situation | Behaviour |
|---|---|
| Runtime unavailable at Start | `503` (v0.1 `runtime.Unavailable` → 503 mapping); no row leak (Deploy marks error). |
| Deploy times out (slow pull) | Row `error`, 120 s cap; Start returns errored instance; UI Retry. |
| Team at quota | `409 quota-exceeded` with the current count and limit in the detail. |
| Extend past max lifetime | `409 max-lifetime-reached`. |
| Event not running | `409 event-not-running` (before start; after end, cleanup removes it). |
| Two members click Start | Mutex + idempotent GetTeamInstance → both get the same instance. |
| Expiry while a member holds the detail page | Next poll/WS sees the instance gone; UI shows "expired — start again". |

## Decision log

- **In-process mutex, DB-enforced invariants.** Simplest correct design for single-process
  v0.2; the DB is the real guard so a future multi-process build only swaps the lock.
- **30 s expiry granularity.** Cheap, and 30 s of slack on an hours-long TTL is invisible.
- **Event-end destroys per-team, keeps shared.** Matches how organizers actually run the
  wind-down; shared instances are the practice surface.
- **Synchronous Start.** A queue is real complexity for no MVP benefit; pre-built images
  make deploy fast, and the 120 s cap already bounds the worst case.
