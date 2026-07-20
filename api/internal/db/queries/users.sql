-- name: CreateUser :one
INSERT INTO users (id, username, email, password_hash, role, hidden)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;

-- name: UpdateUserAdminFields :one
UPDATE users SET
    banned = coalesce(sqlc.narg('banned'), banned),
    hidden = coalesce(sqlc.narg('hidden'), hidden),
    role   = coalesce(sqlc.narg('role'), role),
    updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListUsersAdmin :many
SELECT sqlc.embed(u), t.id AS team_id, t.name AS team_name
FROM users u
LEFT JOIN team_members tm ON tm.user_id = u.id
LEFT JOIN teams t ON t.id = tm.team_id
WHERE (sqlc.narg('q')::text IS NULL
       OR u.username ILIKE '%' || sqlc.narg('q') || '%'
       OR u.email ILIKE '%' || sqlc.narg('q') || '%')
  AND (sqlc.narg('banned')::boolean IS NULL OR u.banned = sqlc.narg('banned'))
  AND (sqlc.narg('hidden')::boolean IS NULL OR u.hidden = sqlc.narg('hidden'))
  AND (sqlc.narg('role')::text IS NULL OR u.role = sqlc.narg('role'))
ORDER BY u.created_at DESC
LIMIT $1 OFFSET $2;

-- name: CountUsersAdmin :one
SELECT count(*) FROM users u
WHERE (sqlc.narg('q')::text IS NULL
       OR u.username ILIKE '%' || sqlc.narg('q') || '%'
       OR u.email ILIKE '%' || sqlc.narg('q') || '%')
  AND (sqlc.narg('banned')::boolean IS NULL OR u.banned = sqlc.narg('banned'))
  AND (sqlc.narg('hidden')::boolean IS NULL OR u.hidden = sqlc.narg('hidden'))
  AND (sqlc.narg('role')::text IS NULL OR u.role = sqlc.narg('role'));

-- name: CountUsers :one
SELECT count(*) FROM users;
