-- +goose Up

-- challenges: per-team instancing, per-instance flags, TTL/egress/writable-paths.
-- All additive with v0.1-equivalent defaults, so existing rows are unchanged in behaviour.
ALTER TABLE challenges
    ADD COLUMN instancing           text    NOT NULL DEFAULT 'shared'
        CHECK (instancing IN ('shared','per_team')),
    ADD COLUMN flag_mode            text    NOT NULL DEFAULT 'static'
        CHECK (flag_mode IN ('static','per_instance')),
    ADD COLUMN instance_ttl_seconds integer               -- NULL = use OSCTF_INSTANCE_TTL; 0 = no TTL
        CHECK (instance_ttl_seconds IS NULL OR instance_ttl_seconds >= 0),
    ADD COLUMN egress               boolean NOT NULL DEFAULT true,   -- false -> per-team net is --internal
    ADD COLUMN writable_paths       jsonb   NOT NULL DEFAULT '[]'::jsonb;

-- per_team / per_instance / ttl / egress-off / writable-paths only make sense for containers.
ALTER TABLE challenges ADD CONSTRAINT chk_challenges_instancing_kind CHECK (
    kind = 'container'
    OR (instancing = 'shared' AND flag_mode = 'static'
        AND instance_ttl_seconds IS NULL AND egress = true
        AND writable_paths = '[]'::jsonb));

-- instances: owner (team), per-instance flag, TTL, per-team network.
ALTER TABLE instances
    ADD COLUMN team_id    uuid REFERENCES teams(id) ON DELETE CASCADE,  -- NULL = shared (v0.1)
    ADD COLUMN flag       text,        -- per-instance flag; secret; NULL for static challenges
    ADD COLUMN expires_at timestamptz, -- scheduler destroys the instance at this time; NULL = no TTL
    ADD COLUMN network    text;        -- per-team docker network name; NULL = shared 'osctf-challenges'

-- Replace "one instance per challenge" with owner-aware partial uniqueness.
ALTER TABLE instances DROP CONSTRAINT uq_instances_challenge;
CREATE UNIQUE INDEX uq_instances_shared   ON instances (challenge_id) WHERE team_id IS NULL;
CREATE UNIQUE INDEX uq_instances_per_team ON instances (challenge_id, team_id) WHERE team_id IS NOT NULL;

-- Scheduler hot paths.
CREATE INDEX idx_instances_expires_at ON instances (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX idx_instances_team       ON instances (team_id)    WHERE team_id IS NOT NULL;

-- Widen the host-port range for many teams x quota. Kept in lock-step with OSCTF_PORT_RANGE.
-- 0001 declares this as an inline (auto-named) column CHECK; drop it by definition, add a named one.
-- +goose StatementBegin
DO $$
DECLARE cn text;
BEGIN
    SELECT conname INTO cn FROM pg_constraint
     WHERE conrelid = 'instances'::regclass AND contype = 'c'
       AND pg_get_constraintdef(oid) LIKE '%30999%';
    IF cn IS NOT NULL THEN
        EXECUTE format('ALTER TABLE instances DROP CONSTRAINT %I', cn);
    END IF;
END $$;
-- +goose StatementEnd
ALTER TABLE instances ADD CONSTRAINT chk_instances_host_port_range
    CHECK (host_port IS NULL OR host_port BETWEEN 30000 AND 32767);

-- +goose Down

-- Restore the original host-port range check.
ALTER TABLE instances DROP CONSTRAINT chk_instances_host_port_range;
ALTER TABLE instances ADD CONSTRAINT instances_host_port_check
    CHECK (host_port IS NULL OR host_port BETWEEN 30000 AND 30999);

-- Restore v0.1 uniqueness. NOTE: requires per-team instances to be drained first
-- (duplicate challenge_id rows would violate uq_instances_challenge). Instances are
-- cattle; the scheduler's cleanup or an admin destroy drains them before a downgrade.
DROP INDEX IF EXISTS idx_instances_team;
DROP INDEX IF EXISTS idx_instances_expires_at;
DROP INDEX IF EXISTS uq_instances_per_team;
DROP INDEX IF EXISTS uq_instances_shared;
ALTER TABLE instances ADD CONSTRAINT uq_instances_challenge UNIQUE (challenge_id);
ALTER TABLE instances
    DROP COLUMN network,
    DROP COLUMN expires_at,
    DROP COLUMN flag,
    DROP COLUMN team_id;

ALTER TABLE challenges DROP CONSTRAINT chk_challenges_instancing_kind;
ALTER TABLE challenges
    DROP COLUMN writable_paths,
    DROP COLUMN egress,
    DROP COLUMN instance_ttl_seconds,
    DROP COLUMN flag_mode,
    DROP COLUMN instancing;
