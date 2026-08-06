-- +goose Up

-- Add the challenge `type` column: which challenge-TYPE decides correctness — the built-in
-- flag comparison, or a plugin-provided checker (v0.3). It is ORTHOGONAL to `kind` (how the
-- challenge deploys: standard vs container). Additive and backward-compatible: existing rows
-- default to the built-in 'standard' type and nothing else changes.
--
-- DELIBERATELY NO CHECK CONSTRAINT ON `type`. The set of valid types is the RUNTIME registry
-- (built-in 'standard'/'container' plus any loaded challenge-type plugins), enforced at write
-- time in the application (internal/challenges validation). A DB CHECK would force a schema
-- migration every time a plugin adds a type, defeating the plugin model. Do NOT "fix" the
-- missing constraint by adding one here — the write-time check is intentional.
ALTER TABLE challenges ADD COLUMN type text NOT NULL DEFAULT 'standard';

-- +goose Down

ALTER TABLE challenges DROP COLUMN type;
