# 01 — Architecture (v0.2 delta)

Same shape as v0.1: one Go process (`platform serve`), a modular monolith with
enforced package boundaries. v0.2 adds **one new package** (`scheduler`), changes a
handful of existing ones, and does **not** touch the overall topology.

## The ownership model (the central idea)

An `instances` row now has a nullable `team_id`:

- `team_id IS NULL` → a **shared** instance (v0.1 behaviour): one per challenge, admin-managed.
- `team_id = <team>` → a **per-team** instance: one per (challenge, team), participant-managed.

A `challenges` row gains `instancing` (`shared | per_team`). The two are consistent by
construction:

- `instancing = shared` → at most one instance, `team_id IS NULL` (unchanged from v0.1).
- `instancing = per_team` → up to one instance **per team**, each `team_id` set; there is
  no shared instance for such a challenge.

Everything else — port allocation, health, reconcile, connection-info rendering — is
already per-row in v0.1 and needs no conceptual change, only to stop assuming "one row
per challenge."

## Package layout — what changes

| Package | Change in v0.2 |
|---|---|
| `scheduler` | **NEW.** Owns per-team instance lifecycle: spawn-on-demand (quota + flag + TTL), extend, TTL-expiry ticker, event-end cleanup. The single writer of per-team lifecycle transitions. Imports `runtime`, `challenges`, `events`, `db`, `clock`; **never** the Docker SDK. |
| `runtime` | `InstanceSpec` gains `TeamID *uuid.UUID`, `Flag string`, `ReadonlyRootfs bool`, `Tmpfs []string`, `WritablePaths []string`, `Internal bool` (egress off). `Manager` gains owner-aware methods (`DeployFor(team,…)`, `DestroyInstance(id)`, `ListForTeam`, per-team network create/gc). Docker impl: read-only rootfs + tmpfs, per-team network, `--internal` when requested. `Reconcile` already row-based; now GCs empty per-team networks and expiry is the scheduler's job. |
| `challenges` | Create/update accept `instancing`, `flag_mode`, `instance_ttl_seconds`, `egress`, `writable_paths`. Validation: `per_team`/`per_instance`/ttl/egress only valid for `kind=container`. |
| `submissions` | Flag comparison becomes flag-mode-aware: `static` → compare to `challenges.flag` (v0.1); `per_instance` → compare to the **submitting team's instance flag**, and raise a **sharing signal** if the flag matches a *different* team's instance flag (see [`05-flags.md`](05-flags.md)). |
| `handlers` | New participant endpoints (`startInstance`, `stopInstance`, `extendInstance`) delegate to `scheduler`. New admin `listInstances`. Participant challenge detail/list include the caller-team's instance. Admin challenge-instance endpoints now apply to `shared` challenges only (see [`06-api.md`](06-api.md)). |
| `events` | No interface change; the scheduler reads phase via the existing `events.Service` to gate spawns and drive event-end cleanup. |
| `audit` | New action `flag.shared` for sharing signals; `instance.spawn` / `instance.expire` / `instance.cleanup`. |
| `metrics` | Add `osctf_instances{state}` per-owner-kind label or a companion `osctf_team_instances`; add `osctf_instance_spawns_total`, `osctf_instance_expiries_total`, `osctf_flag_sharing_signals_total`. |
| `config` | New env (TTLs, quota, isolation) — see [`08-deployment.md`](08-deployment.md). |
| `seed` | `challenge.yaml` parser accepts the new fields; new example challenges. |

**Hard rules (unchanged from v0.1, extended):**

1. The `scheduler` is the **only** package that writes per-team instance lifecycle
   transitions (`pending→running→stopped/expired/error`) and the only one that calls the
   runtime for spawn/destroy of per-team instances. Handlers call the scheduler.
2. `scheduler` talks to containers **only** through `ChallengeRuntime` / `runtime.Manager`.
   It must not import `github.com/docker/docker/*`.
3. Per-instance flags are secrets: **never** logged, never in `audit_log.meta`, never in
   any participant response other than the injected `FLAG` env inside the container.
4. `scoring` stays pure (numbers in, numbers out) — per-instance flags do not touch it.

## Key flows (normative)

### Participant starts an instance (`POST /challenges/{slug}/submit`… no — `/instance`)

1. `POST /api/v0/challenges/{slug}/instance`. Middleware: session → user → team; event
   must be `running`; challenge must be visible, `kind=container`, `instancing=per_team`.
2. Handler → `scheduler.Start(ctx, teamID, challengeID)`:
   a. **Quota:** count the team's running per-team instances; over `OSCTF_TEAM_INSTANCE_QUOTA`
      → `409` (`quota-exceeded`).
   b. **Idempotent:** an existing running instance for (challenge, team) is returned as-is.
   c. **Allocate** a host port (existing lowest-free logic, now across all instances).
   d. **Flag:** if `flag_mode=per_instance`, generate a unique flag and store it on the row
      (before deploy). If `static`, no per-instance flag.
   e. Insert the `instances` row `state=pending`, `team_id=<team>`,
      `expires_at = now + ttl` (per-challenge or default; null if ttl=0).
   f. Call `runtime.Manager.DeployFor(spec)` — per-team network, hardening, `FLAG` injected
      (per-instance flag when set, else `challenges.flag`). Row → `running` / `error`.
3. Deploy is **synchronous** with the v0.1 120 s cap (image pulls). No queue.
4. Response: the instance object incl. `connection_info` and `expires_at`.

### Participant extends / stops

- `POST /challenges/{slug}/instance/extend`: `scheduler.Extend` → `expires_at = min(now+extend, started_at+max_ttl)`; `409` if already at max.
- `DELETE /challenges/{slug}/instance`: `scheduler.Stop` → `runtime.DestroyInstance` +
  delete row (frees the port + quota slot). Team-scoped: a team can only stop its own.

### Scheduler tickers (in `serve`, context-cancelled on shutdown)

| Ticker | Interval | Work |
|---|---|---|
| **expiry** | 30 s | Destroy per-team instances whose `expires_at < now`; audit `instance.expire`. |
| **event-end cleanup** | 15 s (folds into the existing phase ticker) | On the `running→ended` transition, destroy **all** per-team instances; shared instances are left (organizers keep them for practice). |
| runtime reconcile | 60 s (existing) | Now also GCs empty per-team networks and marks orphaned containers. |

Serialization: the scheduler serializes spawn/expiry with a per-process mutex + the DB
unique constraints (correctness comes from the DB; the mutex only avoids wasted work),
exactly like the v0.1 scoreboard recompute.

### Flag submission (per-instance)

1. `POST /challenges/{slug}/submit` — as v0.1, but for `flag_mode=per_instance`:
   a. Resolve the submitting team's instance for this challenge. **No instance** →
      `403` (`no-instance` — "start the challenge first").
   b. Compare (constant-time) against **that team's** `instances.flag`.
   c. If wrong, additionally check whether the provided flag matches a *different* team's
      instance flag → if so, insert the submission (correct=false) **and** raise a
      `flag.shared` sharing signal (audit + metric), noting the owning team. Never reveal
      this to the submitter.
2. `static` challenges are unchanged from v0.1.

## What is deliberately absent (still)

- No message queue, no distributed scheduler — in-process tickers, as v0.1.
- No per-user instances, no instance migration/snapshot.
- No automated anti-cheat action — detection only.
- No new caching layer; per-team instance reads hit Postgres (event scale is fine).
