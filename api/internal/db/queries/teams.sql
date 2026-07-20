-- name: CreateTeam :one
INSERT INTO teams (id, name, invite_code, captain_id)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetTeamByID :one
SELECT * FROM teams WHERE id = $1;

-- name: GetTeamByInviteCode :one
SELECT * FROM teams WHERE invite_code = $1;

-- name: AddTeamMember :exec
INSERT INTO team_members (team_id, user_id) VALUES ($1, $2);

-- name: RemoveTeamMember :exec
DELETE FROM team_members WHERE team_id = $1 AND user_id = $2;

-- name: GetUserTeam :one
SELECT sqlc.embed(t), tm.joined_at
FROM teams t
JOIN team_members tm ON tm.team_id = t.id
WHERE tm.user_id = $1;

-- name: ListTeamMembers :many
SELECT u.id, u.username, u.hidden, tm.joined_at
FROM team_members tm
JOIN users u ON u.id = tm.user_id
WHERE tm.team_id = $1
ORDER BY tm.joined_at ASC;

-- name: CountTeamMembers :one
SELECT count(*) FROM team_members WHERE team_id = $1;

-- name: UpdateTeamName :one
UPDATE teams SET name = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: UpdateTeamInviteCode :one
UPDATE teams SET invite_code = $2, updated_at = now() WHERE id = $1 RETURNING *;

-- name: UpdateTeamCaptain :exec
UPDATE teams SET captain_id = $2, updated_at = now() WHERE id = $1;

-- name: UpdateTeamAdminFields :one
UPDATE teams SET
    banned = coalesce(sqlc.narg('banned'), banned),
    hidden = coalesce(sqlc.narg('hidden'), hidden),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteTeam :exec
DELETE FROM teams WHERE id = $1;

-- name: CountTeamSubmissions :one
SELECT count(*) FROM submissions WHERE team_id = $1;

-- name: ListPublicTeams :many
SELECT t.id, t.name, t.banned,
       (SELECT count(*) FROM team_members tm WHERE tm.team_id = t.id) AS member_count
FROM teams t
WHERE NOT t.hidden
ORDER BY t.created_at ASC;

-- name: ListTeamsAdmin :many
SELECT t.*,
       (SELECT count(*) FROM team_members tm WHERE tm.team_id = t.id) AS member_count
FROM teams t
WHERE (sqlc.narg('q')::text IS NULL OR t.name ILIKE '%' || sqlc.narg('q') || '%')
ORDER BY t.created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountTeamsAdmin :one
SELECT count(*) FROM teams t
WHERE (sqlc.narg('q')::text IS NULL OR t.name ILIKE '%' || sqlc.narg('q') || '%');

-- name: CountTeams :one
SELECT count(*) FROM teams;
