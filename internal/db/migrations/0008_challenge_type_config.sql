-- +goose Up

-- Add `type_config`: per-challenge, author-defined plugin configuration, validated at write time by
-- the challenge type's ValidateConfig. DELIBERATELY NO DB CHECK on its shape — the valid shape is
-- the plugin's, enforced in the application (internal/challenges), the same reasoning as `type`
-- (0005) and `scoring` (0007): a DB constraint cannot express a plugin-defined schema.
--
-- SHARED COLUMN — this is the ONE per-challenge plugin-config store for all plugin kinds. A
-- challenge-type plugin's ValidateConfig/CheckFlag read it now; a scoring plugin's per-challenge
-- params (the reserved sdk.Score.Params) will reuse THIS column when wired, NOT a parallel
-- `scoring_config`. One author-config channel per challenge, keyed by intent inside the map. Do not
-- add a second per-challenge config column.
--
-- Additive and a NO-OP for existing rows: every existing challenge lands on '{}' and nothing else
-- changes (a built-in type carries no config). Locked-at-solve is unaffected — editing type_config
-- changes only how FUTURE submissions are judged; recorded solves keep their recorded values.
ALTER TABLE challenges ADD COLUMN type_config jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down

ALTER TABLE challenges DROP COLUMN type_config;
