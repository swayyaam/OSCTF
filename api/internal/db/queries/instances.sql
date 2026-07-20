-- name: CreateInstance :one
INSERT INTO instances (id, challenge_id, state, host_port)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetInstanceByID :one
SELECT * FROM instances WHERE id = $1;

-- name: GetInstanceByChallenge :one
SELECT * FROM instances WHERE challenge_id = $1;

-- name: UpdateInstance :one
UPDATE instances SET
    state          = coalesce(sqlc.narg('state'), state),
    container_id   = coalesce(sqlc.narg('container_id'), container_id),
    error          = CASE WHEN sqlc.arg('set_error')::boolean
                          THEN sqlc.narg('error') ELSE error END,
    started_at     = coalesce(sqlc.narg('started_at'), started_at),
    last_health_at = coalesce(sqlc.narg('last_health_at'), last_health_at),
    updated_at     = now()
WHERE id = $1
RETURNING *;

-- name: DeleteInstance :exec
DELETE FROM instances WHERE id = $1;

-- name: ListInstances :many
SELECT * FROM instances ORDER BY created_at ASC;

-- name: ListUsedPorts :many
SELECT host_port FROM instances WHERE host_port IS NOT NULL ORDER BY host_port ASC;

-- name: CountInstancesByState :many
SELECT state, count(*) AS n FROM instances GROUP BY state;

-- name: CountRunningInstances :one
SELECT count(*) FROM instances WHERE state = 'running';
