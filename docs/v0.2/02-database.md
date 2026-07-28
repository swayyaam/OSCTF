# 02 — Database (migration `0002_dynamic_instances`)

Postgres stays the source of truth. v0.2 is **one additive, non-destructive migration**:
new nullable columns, one relaxed uniqueness rule, two new indexes. Existing rows keep
working; a v0.1 database upgrades in place. All new SQL lives in
`api/internal/db/migrations/0002_dynamic_instances.sql` and is exercised by the
migration test (up **and** down).

Baseline: [`0001_init.sql`](../../api/internal/db/migrations/0001_init.sql) — read it
first; every reference below is a delta on it.

## `challenges` — new columns

```sql
ALTER TABLE challenges
    ADD COLUMN instancing           text    NOT NULL DEFAULT 'shared'
        CHECK (instancing IN ('shared','per_team')),
    ADD COLUMN flag_mode            text    NOT NULL DEFAULT 'static'
        CHECK (flag_mode IN ('static','per_instance')),
    ADD COLUMN instance_ttl_seconds integer               -- NULL = use OSCTF_INSTANCE_TTL; 0 = no TTL
        CHECK (instance_ttl_seconds IS NULL OR instance_ttl_seconds >= 0),
    ADD COLUMN egress               boolean NOT NULL DEFAULT true,   -- false → per-team net is --internal
    ADD COLUMN writable_paths       jsonb   NOT NULL DEFAULT '[]'::jsonb;  -- extra rw dirs (read-only rootfs)
```

Consistency constraints (added in the same migration):

```sql
-- per_team / per_instance / ttl / egress-off only make sense for container challenges
ALTER TABLE challenges ADD CONSTRAINT chk_challenges_instancing_kind CHECK (
    kind = 'container'
    OR (instancing = 'shared' AND flag_mode = 'static'
        AND instance_ttl_seconds IS NULL AND egress = true
        AND writable_paths = '[]'::jsonb));
```

`flag` stays `NOT NULL` even for `flag_mode = per_instance`: it holds the *template*/fallback
and keeps the column contract stable. For `per_instance` the effective flag is the
per-row `instances.flag`; the column value is never injected when a per-instance flag
exists. (See [`05-flags.md`](05-flags.md).)

**Default-safety:** every new column has a v0.1-equivalent default, so every existing
challenge becomes `shared`/`static`/egress-on with no writable-path additions — identical
behaviour.

## `instances` — new columns + relaxed uniqueness

```sql
ALTER TABLE instances
    ADD COLUMN team_id    uuid REFERENCES teams(id) ON DELETE CASCADE,  -- NULL = shared (v0.1)
    ADD COLUMN flag       text,        -- per-instance flag when challenge.flag_mode='per_instance'; secret
    ADD COLUMN expires_at timestamptz, -- scheduler destroys the instance at this time; NULL = no TTL
    ADD COLUMN network    text;        -- per-team docker network name; NULL = shared 'osctf-challenges' net
```

Replace the "one instance per challenge" rule with an owner-aware pair of partial
unique indexes:

```sql
ALTER TABLE instances DROP CONSTRAINT uq_instances_challenge;
-- at most one shared instance per challenge (v0.1 semantics preserved)
CREATE UNIQUE INDEX uq_instances_shared   ON instances (challenge_id) WHERE team_id IS NULL;
-- at most one instance per (challenge, team)
CREATE UNIQUE INDEX uq_instances_per_team ON instances (challenge_id, team_id) WHERE team_id IS NOT NULL;
```

New indexes for the scheduler's hot paths:

```sql
CREATE INDEX idx_instances_expires_at ON instances (expires_at) WHERE expires_at IS NOT NULL; -- expiry ticker
CREATE INDEX idx_instances_team       ON instances (team_id)    WHERE team_id IS NOT NULL;     -- quota counts
```

Widen the host-port range so a busy event (many teams × quota) does not exhaust it —
must stay in lock-step with `OSCTF_PORT_RANGE` ([`08-deployment.md`](08-deployment.md)):

```sql
ALTER TABLE instances DROP CONSTRAINT chk_instances_host_port_range;  -- if named; else drop+recreate the CHECK
ALTER TABLE instances ADD CONSTRAINT chk_instances_host_port_range
    CHECK (host_port IS NULL OR host_port BETWEEN 30000 AND 32767);
```

> If the v0.1 range check is inline-unnamed on the column, recreate it: the down
> migration must restore the original `30000–30999`. The migration test asserts both.

**Lifecycle & states:** unchanged set (`pending…lost`). Expiry and event-end cleanup use
**Destroy semantics** — the row is deleted (freeing port + quota), exactly like a v0.1
admin destroy. No `expired` state is persisted; the audit log (`instance.expire`,
`instance.cleanup`) is the record. This keeps the state machine identical to v0.1.

## Down migration

Symmetric and tested. Order matters (drop indexes/constraints before columns):

```sql
-- +goose Down
DROP INDEX IF EXISTS idx_instances_team;
DROP INDEX IF EXISTS idx_instances_expires_at;
DROP INDEX IF EXISTS uq_instances_per_team;
DROP INDEX IF EXISTS uq_instances_shared;
ALTER TABLE instances ADD CONSTRAINT uq_instances_challenge UNIQUE (challenge_id);  -- fails if per-team rows exist
ALTER TABLE instances DROP COLUMN network, DROP COLUMN expires_at,
                      DROP COLUMN flag, DROP COLUMN team_id;
-- restore original host-port range
ALTER TABLE challenges DROP CONSTRAINT chk_challenges_instancing_kind;
ALTER TABLE challenges DROP COLUMN writable_paths, DROP COLUMN egress,
                       DROP COLUMN instance_ttl_seconds, DROP COLUMN flag_mode,
                       DROP COLUMN instancing;
```

> The down migration **requires per-team instances to be drained first** (restoring
> `uq_instances_challenge` fails with duplicate `challenge_id`s otherwise). This is
> acceptable: instances are cattle, and a downgrade in an event context is preceded by
> scheduler cleanup. The migration test drains before downgrading and documents this.

## sqlc — query changes

`api/internal/db/queries/instances.sql`, additive (regenerate `db/gen` after):

```sql
-- name: CreateInstance :one   -- CHANGED: add team_id, flag, expires_at, network
INSERT INTO instances (id, challenge_id, team_id, state, host_port, flag, expires_at, network)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetTeamInstance :one   -- NEW: a team's instance for a challenge (per-team path)
SELECT * FROM instances WHERE challenge_id = $1 AND team_id = $2;

-- name: GetSharedInstance :one  -- NEW: rename of GetInstanceByChallenge, team_id IS NULL
SELECT * FROM instances WHERE challenge_id = $1 AND team_id IS NULL;

-- name: ListTeamInstances :many -- NEW: all running instances a team owns (quota + UI)
SELECT * FROM instances WHERE team_id = $1 ORDER BY created_at ASC;

-- name: CountTeamRunningInstances :one  -- NEW: quota check
SELECT count(*) FROM instances WHERE team_id = $1 AND state = 'running';

-- name: ListExpiredInstances :many -- NEW: expiry ticker (running/pending past TTL)
SELECT * FROM instances
WHERE expires_at IS NOT NULL AND expires_at < sqlc.arg('now')
  AND state NOT IN ('stopped','error','lost');

-- name: ListPerTeamInstances :many -- NEW: event-end cleanup (all team-owned instances)
SELECT * FROM instances WHERE team_id IS NOT NULL;

-- name: SetInstanceExpiry :one  -- NEW: extend
UPDATE instances SET expires_at = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: FindInstanceByFlag :one -- NEW: sharing-signal lookup (per-instance flag → owning team)
SELECT * FROM instances WHERE challenge_id = $1 AND flag = $2 AND team_id IS NOT NULL;
```

`GetInstanceByChallenge` (v0.1) is superseded by `GetSharedInstance` for the shared path;
keep it only if a call site still needs "any instance for challenge." `ListUsedPorts`,
`UpdateInstance`, `DeleteInstance`, `ListInstances`, `CountInstancesByState` are unchanged
and now naturally span both owner kinds.

`FindInstanceByFlag` returns a **secret**; its result is used only to derive the owning
team id for the sharing signal and is never serialized to a response.

## Redis

**No new keys.** The scheduler is in-process and DB-backed; correctness comes from the
partial unique indexes and the `expires_at`/`team_id` predicates above, plus a
per-process mutex (see [`04-scheduler.md`](04-scheduler.md)). Sessions and rate-limit
keys are unchanged from v0.1. (A distributed scheduler with a Redis lease is a v0.4
concern, explicitly out of scope.)

## Decision log

- **Expiry deletes the row (no `expired` state).** Keeps the v0.1 state machine intact and
  frees port + quota atomically; the audit log is the historical record.
- **`flag` stays `NOT NULL` on `challenges`.** Avoids a nullable-column contract change;
  per-instance flags live on `instances.flag` and shadow it when present.
- **Partial unique indexes over a composite PK change.** Preserves v0.1 shared semantics
  byte-for-byte while adding per-team uniqueness, with zero data rewrite.
- **Host-port range widened to 30000–32767.** Headroom for many teams × quota; kept in
  sync with `OSCTF_PORT_RANGE`. Larger events that still exhaust it are a documented
  operational limit, not a v0.2 feature.
