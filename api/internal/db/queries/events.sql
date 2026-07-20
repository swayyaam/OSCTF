-- name: GetEvent :one
SELECT * FROM events ORDER BY created_at ASC LIMIT 1;

-- name: CreateEvent :one
INSERT INTO events (id, name, description, starts_at, ends_at, freeze_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateEvent :one
UPDATE events SET
    name        = coalesce(sqlc.narg('name'), name),
    description = coalesce(sqlc.narg('description'), description),
    starts_at   = coalesce(sqlc.narg('starts_at'), starts_at),
    ends_at     = coalesce(sqlc.narg('ends_at'), ends_at),
    freeze_at   = CASE WHEN sqlc.arg('set_freeze')::boolean
                       THEN sqlc.narg('freeze_at')
                       ELSE freeze_at END,
    updated_at  = now()
WHERE id = $1
RETURNING *;

-- name: CountEvents :one
SELECT count(*) FROM events;
