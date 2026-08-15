-- +goose Up

-- v0.2 stored the raw submitted flag in submissions.provided for every attempt. For a
-- per_instance challenge that persisted a per-team instance flag whenever the submitted
-- value WAS a real instance flag — the team's own (a correct solve) or, via flag sharing,
-- another team's — and the admin submissions view echoed it back. v0.2.1 redacts these at
-- write time (submissions.Submit); this backfills existing rows using the same condition
-- the new code applies: the submission is correct, OR its provided value still matches a
-- known instance flag for that challenge (the sharing case). Genuinely wrong guesses are
-- left intact for triage.
--
-- Best-effort, matching the runtime detector's own limitation: a shared flag whose
-- instance has since been destroyed can no longer be matched by value and is left as-is.
-- Correct solves are always caught (they are flagged correct regardless of the instance's
-- current state), so a team's OWN instance flag is never left exposed.
UPDATE submissions s
SET provided = '[redacted per-instance flag]'
FROM challenges c
WHERE s.challenge_id = c.id
  AND c.flag_mode = 'per_instance'
  AND s.provided <> '[redacted per-instance flag]'
  AND (
    s.correct
    OR EXISTS (
      SELECT 1 FROM instances i
      WHERE i.challenge_id = s.challenge_id AND i.flag = s.provided
    )
  );

-- +goose Down

-- Irreversible: the original per-instance flag values were intentionally discarded.
SELECT 1;
