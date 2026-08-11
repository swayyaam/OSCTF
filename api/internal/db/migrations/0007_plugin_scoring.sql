-- +goose Up

-- Allow plugin-provided scoring modes. Like `type` (0005), the valid set of scoring modes is the
-- RUNTIME registry (built-in 'static'/'dynamic' plus any loaded scoring plugins), enforced at
-- write time in the application (internal/challenges validation via scoring.IsRegisteredMode). A
-- DB CHECK would force a schema migration every time a plugin adds a mode, defeating the plugin
-- model. Do NOT restore this CHECK — the write-time registry check REPLACES it (it does not just
-- succeed it), exactly as for `type`.
ALTER TABLE challenges DROP CONSTRAINT IF EXISTS challenges_scoring_check;

-- Per-solve scoring record for PLUGIN-scored challenges (locked-at-solve, computed post-commit).
-- Plugin scoring is off the read path: the value is recorded on the solve here, and the
-- scoreboard recompute READS it — so served == a from-scratch recompute over (log + records) even
-- when the plugin is down. `scored_by` names the engine that produced the value:
--   '<plugin-mode>' — the plugin's value;  'fallback' — static value (opt-in fallback fired);
--   'pending'       — no value yet (fallback off + plugin down), a DEFINITE state resolving to 0.
-- Both columns are NULL for wrong attempts and for built-in static/dynamic solves, which recompute
-- from the formula. A valid plugin-scored solve with NULL scored_value is a MISSING record (the
-- post-commit write failed) — the recompute resolves it to the deterministic default and a bounded
-- background worker fills it; missing is counted separately from pending.
ALTER TABLE submissions ADD COLUMN scored_value integer;
ALTER TABLE submissions ADD COLUMN scored_by    text;

-- +goose Down

ALTER TABLE submissions DROP COLUMN scored_by;
ALTER TABLE submissions DROP COLUMN scored_value;
ALTER TABLE challenges ADD CONSTRAINT challenges_scoring_check CHECK (scoring IN ('static','dynamic'));
