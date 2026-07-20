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
SELECT s.team_id, s.challenge_id, s.created_at AS solved_at,
       t.name AS team_name, t.banned AS team_banned,
       c.scoring, c.points_initial, c.points_min, c.decay
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
