# 06 — API changes

The API stays spec-first: `api/openapi/openapi.yaml` is the source of truth, `apigen`
(strict server) and the `openapi-typescript` client are regenerated and checked in, and the
`generate-drift` CI gate fails on any uncommitted diff. All v0.2 endpoints live under
`/api/v0` (still unstable pre-Phase-3). Baseline: [`../v0.1/05-api.md`](../v0.1/05-api.md).

## New participant endpoints (per-team instance controls)

For `container` challenges with `instancing = per_team`. All require an authenticated user
**on a team**, and (for Start/Extend) the event in the `running` phase.

| Method + path | operationId | Purpose |
|---|---|---|
| `POST /challenges/{slug}/instance` | `startInstance` | Start (or return) this team's instance. |
| `DELETE /challenges/{slug}/instance` | `stopInstance` | Stop and destroy this team's instance. |
| `POST /challenges/{slug}/instance/extend` | `extendInstance` | Extend this team's instance TTL. |

Responses use a new **`TeamInstance`** schema (superset of `Instance`, all participant-safe
— **no flag**):

```yaml
TeamInstance:
  type: object
  required: [id, state]
  properties:
    id:              { type: string, format: uuid }
    state:           { $ref: '#/components/schemas/InstanceState' }
    host_port:       { type: integer, nullable: true }
    connection_info: { type: string,  nullable: true }
    started_at:      { type: string, format: date-time, nullable: true }
    expires_at:      { type: string, format: date-time, nullable: true }  # NEW: countdown source
    error:           { type: string, nullable: true }
```

Status codes:

| Code | When |
|---|---|
| `200` startInstance | Existing running instance returned (idempotent). |
| `201` startInstance | New instance deployed. |
| `202`/`200` with `state:pending` | Deploy still settling (rare; frontend polls). |
| `409 event-not-running` | Event not in `running` phase. |
| `409 quota-exceeded` | Team at `OSCTF_TEAM_INSTANCE_QUOTA`; detail has `limit`+`current`. |
| `409 max-lifetime-reached` | extendInstance past `OSCTF_INSTANCE_MAX_TTL`. |
| `404 no-instance` | stop/extend with no instance for this team. |
| `409 not-per-team` | Challenge is `shared`/`standard` (wrong endpoint). |
| `503` | Runtime unavailable. |

All errors use the existing RFC-7807 `Problem` envelope with a stable `type` slug (as v0.1).

## Changed participant endpoints

### `getChallenge` (`GET /challenges/{slug}`)

`ChallengeDetail` gains (all additive, safe):

```yaml
instancing:  { type: string, enum: [shared, per_team] }   # so the UI shows Start controls
flag_mode:   { type: string, enum: [static, per_instance] } # optional/informational
instance:    { $ref: '#/components/schemas/TeamInstance', nullable: true }  # caller-team's instance
```

For `shared` challenges, `instance` continues to reflect the shared instance (v0.1
`Instance` shape is compatible with `TeamInstance` — `expires_at` is simply null). For
`per_team`, `instance` is **this caller's team's** instance (or null → show Start).

### `listChallenges` (`GET /challenges`)

Add `instancing` to each `ChallengeSummary` so the list can badge per-team challenges.
Do **not** embed per-team instance state in the list (N queries); the detail view owns it.

### `submitFlag` (`POST /challenges/{slug}/submit`)

Wire-compatible. Behaviour change is server-side only (per [`05-flags.md`](05-flags.md)):
`per_instance` challenges compare against the team's instance flag and may add a `403
no-instance`. The response schema (`SubmitResult`) is unchanged.

## New/changed admin endpoints (observability)

| Method + path | operationId | Purpose |
|---|---|---|
| `GET /admin/instances` | `adminListInstances` | Every instance (shared + per-team) with owner, state, port, age, expiry, health. |
| `DELETE /admin/instances/{id}` | `adminDestroyInstanceById` | Destroy **any** instance by id (skips ownership). |

`AdminInstance` schema (admin-only — still **no flag**):

```yaml
AdminInstance:
  type: object
  required: [id, challenge_id, state]
  properties:
    id:            { type: string, format: uuid }
    challenge_id:  { type: string, format: uuid }
    challenge_slug:{ type: string }
    team_id:       { type: string, format: uuid, nullable: true }   # null = shared
    team_name:     { type: string, nullable: true }
    state:         { $ref: '#/components/schemas/InstanceState' }
    host_port:     { type: integer, nullable: true }
    network:       { type: string, nullable: true }
    started_at:    { type: string, format: date-time, nullable: true }
    expires_at:    { type: string, format: date-time, nullable: true }
    last_health_at:{ type: string, format: date-time, nullable: true }
    error:         { type: string, nullable: true }
```

The **existing** `/admin/challenges/{id}/instance*` endpoints (deploy/get/destroy/restart/
logs) remain for **shared** challenges. For a `per_team` challenge, `adminDeployInstance`
returns `409 not-shared` (per-team instances are participant/scheduler-driven); logs/get by
challenge are ambiguous for per-team and are superseded by `/admin/instances` + a per-id
logs route if needed. Keep changes minimal: v0.1 admin instance ops keep working for shared
challenges unchanged.

### `adminCreateChallenge` / `adminUpdateChallenge`

`AdminChallengeInput` gains the new authoring fields (all optional, defaulting to v0.1):

```yaml
instancing:           { type: string, enum: [shared, per_team], default: shared }
flag_mode:            { type: string, enum: [static, per_instance], default: static }
instance_ttl_seconds: { type: integer, nullable: true, minimum: 0 }  # null=default TTL, 0=no TTL
egress:               { type: boolean, default: true }
writable_paths:       { type: array, items: { type: string }, default: [] }
```

Server validation mirrors the DB constraint ([`02-database.md`](02-database.md)): these are
only accepted for `kind=container`; a `422` (validation problem) otherwise. `adminGetChallenge`
returns them so the editor round-trips.

### `adminListSubmissions`

Add an optional `sharing_signal` boolean filter and surface a `shared` badge derived from
the `flag.shared` audit action, so organizers can review sharing signals in the existing
submissions view. (No schema break: an additive optional query param + an additive response
field.)

## WebSocket

The v0.1 hub broadcasts scoreboard + phase. v0.2 adds a **per-connection instance nudge**
so a participant's challenge page updates without polling:

- New event `instance` with `{challenge_id, state, expires_at}` sent to the owning team's
  connections when the scheduler changes one of their instances (spawn done, expiry, stop).
- Keep it minimal: the client treats it as a signal to refetch `getChallenge` (same pattern
  as v0.1 scoreboard invalidation), so no secret ever rides the socket.
- If per-connection team targeting is more than a small change to the existing hub, fall
  back to a lightweight client poll of `getChallenge` while an instance is `pending` or has
  a live countdown — the frontend already polls in v0.1 patterns. Document whichever is
  built in the decision log.

## OpenAPI mechanics (unchanged workflow)

1. Edit `api/openapi/openapi.yaml` (add paths/schemas above).
2. `make generate` (or the documented codegen target) → regenerates `apigen` + TS client.
3. Implement the strict-server handlers in `internal/handlers`, delegating to `scheduler`
   (instance ops), `submissions` (submit), `challenges` (authoring).
4. Commit generated code; `generate-drift` CI must be clean.

## Decision log

- **`TeamInstance` superset, never a flag field.** One participant-safe shape for shared and
  per-team; `expires_at` drives the countdown. Flags are excluded at the schema level so
  they cannot leak by accident.
- **New `/admin/instances` collection, keep per-challenge admin ops for shared.** Avoids
  overloading the challenge-scoped routes (which assume one instance) while preserving v0.1
  admin behaviour.
- **Submit stays wire-compatible.** The only participant-visible addition is a `403
  no-instance`; existing clients for static challenges are unaffected.
- **WS instance nudge is a refetch signal, not data.** Consistent with v0.1's
  "socket carries deltas, REST carries state," and keeps secrets off the wire.
