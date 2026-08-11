-- name: CreateSubmission :one
INSERT INTO submissions (id, challenge_id, team_id, user_id, provided, correct, ip)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: HasTeamSolved :one
SELECT EXISTS (
    SELECT 1 FROM submissions WHERE challenge_id = $1 AND team_id = $2 AND correct
);

-- name: CountTeamAttempts :one
SELECT count(*) FROM submissions WHERE challenge_id = $1 AND team_id = $2;

-- name: ListValidSolves :many
-- The one scoreboard input query (docs/v0.1/07-scoring.md): every correct
-- submission for a visible challenge by a non-hidden team with at least one
-- non-hidden member. Banned teams are INCLUDED and flagged; Go excludes them
-- from solve counts but still displays them.
--
-- scored_value carries the LOCKED-AT-SOLVE value for a plugin-scored challenge (0007). The
-- recompute reads it for plugin modes instead of calling the plugin — so the served board equals
-- a from-scratch recompute over (log + records) even when the plugin is down. Built-in
-- static/dynamic ignore it and recompute from the formula; a NULL on a plugin solve is a MISSING
-- record (resolves to the deterministic default on read, a background worker fills it).
SELECT s.team_id, s.challenge_id, s.created_at AS solved_at,
       t.name AS team_name, t.banned AS team_banned,
       c.scoring, c.points_initial, c.points_min, c.decay,
       s.scored_value
FROM submissions s
JOIN teams t ON t.id = s.team_id
JOIN challenges c ON c.id = s.challenge_id
WHERE s.correct
  AND c.visible
  AND NOT t.hidden
  AND EXISTS (
      SELECT 1 FROM team_members tm
      JOIN users u ON u.id = tm.user_id
      WHERE tm.team_id = t.id AND NOT u.hidden
  )
ORDER BY s.created_at ASC;

-- name: RecordScore :execrows
-- Records the locked-at-solve value for a plugin-scored solve. Written post-commit on the write
-- path, and by the off-read-path repair worker for MISSING/PENDING records. The `scored_value IS
-- NULL` guard makes it a write-once from the read path's view: a repair tick that raced a fresh
-- write-path record (which already set a value) is a no-op (0 rows), never a clobber. scored_by
-- names the source: the plugin mode, 'fallback' (static value used), or 'pending' (deferred → 0).
UPDATE submissions
SET scored_value = sqlc.narg('scored_value'), scored_by = $2
WHERE id = $1 AND correct AND scored_value IS NULL;

-- name: ListSolvesNeedingScore :many
-- Correct solves on a PLUGIN-scored challenge with no recorded value yet — MISSING (scored_by
-- NULL, the post-commit write never landed) or PENDING (scored_by 'pending', deferred while the
-- plugin was down). The off-read-path repair worker reads these on a tick and records a value.
-- No visibility filter: the record is a durable per-solve fact, independent of whether the
-- challenge is currently visible.
SELECT s.id, s.challenge_id, c.scoring, c.points_initial, c.points_min, c.decay
FROM submissions s
JOIN challenges c ON c.id = s.challenge_id
WHERE s.correct
  AND c.scoring NOT IN ('static', 'dynamic')
  AND s.scored_value IS NULL
ORDER BY s.created_at ASC
LIMIT $1;

-- name: CountUnscoredPluginSolves :one
-- The durability signal for plugin scoring: MISSING (scored_by NULL — the post-commit write
-- failed, an absence that must be ALERTABLE) counted separately from PENDING (scored_by 'pending'
-- — a deferred value while the plugin was down, expected to clear when it recovers). Same set as
-- ListSolvesNeedingScore. A sustained missing count means the write path is broken.
SELECT
    count(*) FILTER (WHERE s.scored_by IS NULL)      AS missing,
    count(*) FILTER (WHERE s.scored_by = 'pending')  AS pending
FROM submissions s
JOIN challenges c ON c.id = s.challenge_id
WHERE s.correct
  AND c.scoring NOT IN ('static', 'dynamic')
  AND s.scored_value IS NULL;

-- name: CountValidSolves :one
-- The read-repair version marker: the number of valid-solve rows the board is computed
-- from (identical filter to ListValidSolves, count only). A served snapshot records the
-- count it was built from; if this has moved past it, the board is stale and Current()
-- recomputes before serving. Same data-derived "newer" as the write guard (docs/v0.3).
SELECT count(*) FROM submissions s
JOIN teams t ON t.id = s.team_id
JOIN challenges c ON c.id = s.challenge_id
WHERE s.correct
  AND c.visible
  AND NOT t.hidden
  AND EXISTS (
      SELECT 1 FROM team_members tm
      JOIN users u ON u.id = tm.user_id
      WHERE tm.team_id = t.id AND NOT u.hidden
  );

-- name: ListScoreboardTeams :many
-- Every non-hidden team appears on the board from creation (zero-solve teams too).
SELECT t.id, t.name, t.banned FROM teams t WHERE NOT t.hidden ORDER BY t.created_at ASC;

-- name: ListTeamSolves :many
SELECT s.challenge_id, s.created_at AS solved_at, u.username,
       c.slug, c.title, c.category
FROM submissions s
JOIN users u ON u.id = s.user_id
JOIN challenges c ON c.id = s.challenge_id
WHERE s.correct AND s.team_id = $1 AND c.visible
ORDER BY s.created_at ASC;

-- name: ListUserSolves :many
SELECT s.challenge_id, s.created_at AS solved_at,
       c.slug, c.title, c.category
FROM submissions s
JOIN challenges c ON c.id = s.challenge_id
WHERE s.correct AND s.user_id = $1 AND c.visible
ORDER BY s.created_at ASC;

-- name: ListSubmissionsAdmin :many
SELECT s.*, c.slug AS challenge_slug, c.title AS challenge_title,
       t.name AS team_name, u.username
FROM submissions s
JOIN challenges c ON c.id = s.challenge_id
JOIN teams t ON t.id = s.team_id
JOIN users u ON u.id = s.user_id
WHERE (sqlc.narg('challenge_id')::uuid IS NULL OR s.challenge_id = sqlc.narg('challenge_id'))
  AND (sqlc.narg('team_id')::uuid IS NULL OR s.team_id = sqlc.narg('team_id'))
  AND (sqlc.narg('user_id')::uuid IS NULL OR s.user_id = sqlc.narg('user_id'))
  AND (sqlc.narg('correct')::boolean IS NULL OR s.correct = sqlc.narg('correct'))
  AND (sqlc.narg('since')::timestamptz IS NULL OR s.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::timestamptz IS NULL OR s.created_at < sqlc.narg('until'))
ORDER BY s.created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountSubmissionsAdmin :one
SELECT count(*) FROM submissions s
WHERE (sqlc.narg('challenge_id')::uuid IS NULL OR s.challenge_id = sqlc.narg('challenge_id'))
  AND (sqlc.narg('team_id')::uuid IS NULL OR s.team_id = sqlc.narg('team_id'))
  AND (sqlc.narg('user_id')::uuid IS NULL OR s.user_id = sqlc.narg('user_id'))
  AND (sqlc.narg('correct')::boolean IS NULL OR s.correct = sqlc.narg('correct'))
  AND (sqlc.narg('since')::timestamptz IS NULL OR s.created_at >= sqlc.narg('since'))
  AND (sqlc.narg('until')::timestamptz IS NULL OR s.created_at < sqlc.narg('until'));

-- name: CountSubmissions :one
SELECT count(*) FROM submissions;

-- name: CountSolves :one
SELECT count(*) FROM submissions WHERE correct;

-- name: CountChallengeSolves :one
SELECT count(*) FROM submissions WHERE challenge_id = $1 AND correct;
