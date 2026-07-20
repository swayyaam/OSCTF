# 04 — Database Schema

PostgreSQL 17 (compose pins the image; any ≥ 16 works). This file specifies the **initial migration set**. Once migrations exist in `api/internal/db/migrations/`, the repo is authoritative.

## Conventions

- IDs: `uuid` primary keys, generated **in the application** with UUID v7 (time-ordered → index-friendly). No DB-side uuid generation.
- Timestamps: `timestamptz`, always UTC. Every table has `created_at timestamptz not null default now()`; mutable tables add `updated_at` (set by the application on update; no triggers).
- Naming: `snake_case`, singular column names, plural table names. FKs named `<entity>_id`. Indexes `idx_<table>_<cols>`, unique `uq_<table>_<cols>`.
- Case-insensitive uniqueness (usernames, emails, team names) via the `citext` extension.
- Money/points: plain `integer`. Point values fit comfortably.
- Enum-like fields: `text` + `CHECK` constraint (easier to migrate than native enums).
- Migrations: goose, sequential numeric prefix (`0001_init.sql`, `0002_...`), each with `-- +goose Up` / `-- +goose Down`. Down migrations required and tested in CI (up-down-up).

## Schema (DDL)

Ship as `0001_init.sql` (split later at your discretion; keep ordering below for FK correctness).

```sql
-- +goose Up
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id            uuid PRIMARY KEY,
    username      citext NOT NULL,
    email         citext NOT NULL,
    password_hash text   NOT NULL,             -- PHC string: $argon2id$v=19$m=65536,t=3,p=4$...
    role          text   NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin')),
    banned        boolean NOT NULL DEFAULT false,
    hidden        boolean NOT NULL DEFAULT false,  -- excluded from scoreboard & solve counts
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_users_username UNIQUE (username),
    CONSTRAINT uq_users_email    UNIQUE (email),
    CONSTRAINT chk_users_username_len CHECK (char_length(username) BETWEEN 3 AND 32)
);

CREATE TABLE teams (
    id          uuid PRIMARY KEY,
    name        citext NOT NULL,
    invite_code text   NOT NULL,               -- 12 chars, crockford base32, app-generated
    captain_id  uuid   NOT NULL REFERENCES users(id),
    banned      boolean NOT NULL DEFAULT false,
    hidden      boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_teams_name        UNIQUE (name),
    CONSTRAINT uq_teams_invite_code UNIQUE (invite_code),
    CONSTRAINT chk_teams_name_len CHECK (char_length(name) BETWEEN 3 AND 48)
);

CREATE TABLE team_members (
    team_id   uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id   uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id),
    CONSTRAINT uq_team_members_user UNIQUE (user_id)   -- a user is on at most one team
);

CREATE TABLE events (
    id          uuid PRIMARY KEY,
    name        text NOT NULL,
    description text NOT NULL DEFAULT '',      -- markdown, shown on landing page
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz NOT NULL,
    freeze_at   timestamptz,                   -- NULL = no freeze; must be within window
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_events_window CHECK (starts_at < ends_at),
    CONSTRAINT chk_events_freeze CHECK (freeze_at IS NULL OR (freeze_at > starts_at AND freeze_at <= ends_at))
);
-- v0.1 enforces a single event row in the service layer (first row wins; API is
-- GET/PATCH /event). The schema supports many for later phases.

CREATE TABLE challenges (
    id                    uuid PRIMARY KEY,
    slug                  text NOT NULL,       -- url-safe: ^[a-z0-9]+(-[a-z0-9]+)*$, app-validated
    title                 text NOT NULL,
    category              text NOT NULL CHECK (category IN
                              ('web','pwn','crypto','rev','forensics','misc')),
    description           text NOT NULL DEFAULT '',   -- markdown
    difficulty            text CHECK (difficulty IN ('intro','easy','medium','hard','insane')),
    kind                  text NOT NULL DEFAULT 'standard' CHECK (kind IN ('standard','container')),
    flag                  text NOT NULL,
    flag_case_insensitive boolean NOT NULL DEFAULT false,
    scoring               text NOT NULL DEFAULT 'dynamic' CHECK (scoring IN ('static','dynamic')),
    points_initial        integer NOT NULL CHECK (points_initial > 0),
    points_min            integer,             -- required when scoring='dynamic'
    decay                 integer,             -- required when scoring='dynamic'; solves to reach min
    max_attempts          integer CHECK (max_attempts IS NULL OR max_attempts > 0),  -- NULL = unlimited
    visible               boolean NOT NULL DEFAULT false,
    -- container-kind fields (NULL for standard kind):
    image                 text,                -- e.g. 'osctf/example-cookie-monster:0.1'
    internal_port         integer CHECK (internal_port IS NULL OR internal_port BETWEEN 1 AND 65535),
    mem_limit_mb          integer NOT NULL DEFAULT 256 CHECK (mem_limit_mb BETWEEN 16 AND 8192),
    cpu_millis            integer NOT NULL DEFAULT 500 CHECK (cpu_millis BETWEEN 100 AND 8000),
    container_env         jsonb NOT NULL DEFAULT '{}'::jsonb,   -- extra env for the container
    connection_template   text,                -- e.g. 'nc {host} {port}' or 'http://{host}:{port}'
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_challenges_slug UNIQUE (slug),
    CONSTRAINT chk_challenges_dynamic_fields CHECK (
        scoring <> 'dynamic' OR (points_min IS NOT NULL AND decay IS NOT NULL
                                 AND points_min > 0 AND points_min <= points_initial AND decay > 0)),
    CONSTRAINT chk_challenges_container_fields CHECK (
        kind <> 'container' OR (image IS NOT NULL AND internal_port IS NOT NULL))
);

CREATE TABLE challenge_attachments (
    id           uuid PRIMARY KEY,
    challenge_id uuid NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    filename     text NOT NULL,                -- as shown to users; unique per challenge
    size_bytes   bigint NOT NULL CHECK (size_bytes >= 0),
    content_type text NOT NULL DEFAULT 'application/octet-stream',
    object_key   text NOT NULL,                -- 'challenges/{challenge_id}/{attachment_id}/{filename}'
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_attachments_challenge_filename UNIQUE (challenge_id, filename)
);

CREATE TABLE submissions (
    id           uuid PRIMARY KEY,
    challenge_id uuid NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    team_id      uuid NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users(id),
    provided     text NOT NULL,                -- the submitted flag text, verbatim
    correct      boolean NOT NULL,
    ip           inet,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX uq_submissions_solve ON submissions (challenge_id, team_id) WHERE correct;
CREATE INDEX idx_submissions_team_challenge ON submissions (team_id, challenge_id);
CREATE INDEX idx_submissions_challenge_correct ON submissions (challenge_id) WHERE correct;
CREATE INDEX idx_submissions_created_at ON submissions (created_at);

CREATE TABLE instances (
    id             uuid PRIMARY KEY,
    challenge_id   uuid NOT NULL REFERENCES challenges(id) ON DELETE CASCADE,
    state          text NOT NULL CHECK (state IN
                       ('pending','starting','running','unhealthy','stopped','error','lost')),
    container_id   text,                       -- docker container ID once created
    host_port      integer CHECK (host_port IS NULL OR host_port BETWEEN 30000 AND 30999),
    error          text,                       -- last failure message, for the admin UI
    started_at     timestamptz,
    last_health_at timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_instances_challenge UNIQUE (challenge_id),   -- v0.1: one shared instance
    CONSTRAINT uq_instances_host_port UNIQUE (host_port)
);

CREATE TABLE audit_log (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id   uuid REFERENCES users(id) ON DELETE SET NULL,
    action     text NOT NULL,                  -- 'challenge.create', 'user.ban', 'instance.deploy', ...
    subject_type text NOT NULL,                -- 'challenge' | 'user' | 'team' | 'event' | 'instance'
    subject_id text NOT NULL,
    meta       jsonb NOT NULL DEFAULT '{}'::jsonb,   -- diff/context; NEVER flags or password hashes
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_log_created_at ON audit_log (created_at);

-- +goose Down
DROP TABLE audit_log, instances, submissions, challenge_attachments,
           challenges, events, team_members, teams, users;
DROP EXTENSION IF EXISTS citext;
```

## Semantics & lifecycle rules

- **Solves** are not a separate table: a solve is `submissions WHERE correct`, guaranteed unique per (challenge, team) by the partial index. Solve time = that row's `created_at`.
- **Deleting a challenge** cascades submissions, attachments, and its instance row (the service must `Destroy` the container first, then delete). Deleting a *visible* challenge during a running event requires a `?confirm=true` query param — it rewrites history.
- **Deleting users/teams**: blocked once they have submissions (return `409`); use `banned`/`hidden` instead. Pre-event cleanup deletes are fine.
- **Banned** user: cannot log in (sessions revoked on ban). Banned team: members can log in but cannot submit; team stays visible on the scoreboard struck-through (frontend concern) but is excluded from dynamic solve counts and prize-relevant standings — same exclusion set as `hidden`, plus the visual strike.
- **Sessions are not in Postgres** — they live in Redis (see [`06-auth.md`](06-auth.md)).
- **Team captain leaving**: if other members remain, captaincy transfers to the earliest `joined_at` member; if empty, the team row is deleted when it has no submissions, else it remains (hidden from team-less operations) with `captain_id` unchanged as a historical record. Keep this logic in `teams` service with tests.

## Redis keyspace (for reference; not a schema)

| Key | Type | TTL | Purpose |
|---|---|---|---|
| `sess:{token}` | hash | 168 h sliding | Session: `user_id`, `role`, `created_at`, `ip`, `ua` |
| `rl:{scope}:{key}` | zset | window | Sliding-window rate limits (scope: `login-ip`, `login-acct`, `sub-team-chal`, `sub-user`) |
| `scoreboard:current` | string (JSON) | none | Live standings snapshot |
| `scoreboard:frozen` | string (JSON) | none | Snapshot written at freeze time |

## sqlc

- Queries live in `api/internal/db/queries/<domain>.sql`, one file per domain (`users.sql`, `teams.sql`, …), annotated `-- name: GetUserByEmail :one` style.
- `sqlc.yaml`: engine `postgresql`, `sql_package: pgx/v5`, emit JSON tags off (domain structs map explicitly), output `internal/db/gen`.
- Scoreboard standings are computed by **one SQL query** (aggregate solves joined to challenges and teams, excluding hidden/banned) plus in-Go value computation via `ScoringEngine` — see [`07-scoring.md`](07-scoring.md) for the exact algorithm. Don't put the decay formula in SQL; it belongs to the engine.
