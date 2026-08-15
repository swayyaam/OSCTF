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
DROP TABLE IF EXISTS audit_log, instances, submissions, challenge_attachments,
           challenges, events, team_members, teams, users;
DROP EXTENSION IF EXISTS citext;
