-- name: CreateChallenge :one
INSERT INTO challenges (
    id, slug, title, category, description, difficulty, kind,
    flag, flag_case_insensitive, scoring, points_initial, points_min, decay,
    max_attempts, visible, image, internal_port, mem_limit_mb, cpu_millis,
    container_env, connection_template,
    instancing, flag_mode, instance_ttl_seconds, egress, writable_paths
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18, $19, $20, $21, $22, $23, $24, $25, $26
)
RETURNING *;

-- name: GetChallengeByID :one
SELECT * FROM challenges WHERE id = $1;

-- name: GetChallengeBySlug :one
SELECT * FROM challenges WHERE slug = $1;

-- name: UpdateChallenge :one
UPDATE challenges SET
    slug                  = coalesce(sqlc.narg('slug'), slug),
    title                 = coalesce(sqlc.narg('title'), title),
    category              = coalesce(sqlc.narg('category'), category),
    description           = coalesce(sqlc.narg('description'), description),
    difficulty            = CASE WHEN sqlc.arg('set_difficulty')::boolean
                                 THEN sqlc.narg('difficulty') ELSE difficulty END,
    flag                  = coalesce(sqlc.narg('flag'), flag),
    flag_case_insensitive = coalesce(sqlc.narg('flag_case_insensitive'), flag_case_insensitive),
    scoring               = coalesce(sqlc.narg('scoring'), scoring),
    points_initial        = coalesce(sqlc.narg('points_initial'), points_initial),
    points_min            = CASE WHEN sqlc.arg('set_points_min')::boolean
                                 THEN sqlc.narg('points_min') ELSE points_min END,
    decay                 = CASE WHEN sqlc.arg('set_decay')::boolean
                                 THEN sqlc.narg('decay') ELSE decay END,
    max_attempts          = CASE WHEN sqlc.arg('set_max_attempts')::boolean
                                 THEN sqlc.narg('max_attempts') ELSE max_attempts END,
    visible               = coalesce(sqlc.narg('visible'), visible),
    image                 = CASE WHEN sqlc.arg('set_image')::boolean
                                 THEN sqlc.narg('image') ELSE image END,
    internal_port         = CASE WHEN sqlc.arg('set_internal_port')::boolean
                                 THEN sqlc.narg('internal_port') ELSE internal_port END,
    mem_limit_mb          = coalesce(sqlc.narg('mem_limit_mb'), mem_limit_mb),
    cpu_millis            = coalesce(sqlc.narg('cpu_millis'), cpu_millis),
    container_env         = coalesce(sqlc.narg('container_env'), container_env),
    connection_template   = CASE WHEN sqlc.arg('set_connection_template')::boolean
                                 THEN sqlc.narg('connection_template') ELSE connection_template END,
    instancing            = coalesce(sqlc.narg('instancing'), instancing),
    flag_mode             = coalesce(sqlc.narg('flag_mode'), flag_mode),
    instance_ttl_seconds  = CASE WHEN sqlc.arg('set_instance_ttl_seconds')::boolean
                                 THEN sqlc.narg('instance_ttl_seconds') ELSE instance_ttl_seconds END,
    egress                = coalesce(sqlc.narg('egress'), egress),
    writable_paths        = coalesce(sqlc.narg('writable_paths'), writable_paths),
    updated_at            = now()
WHERE id = $1
RETURNING *;

-- name: DeleteChallenge :exec
DELETE FROM challenges WHERE id = $1;

-- name: ListVisibleChallenges :many
SELECT * FROM challenges WHERE visible ORDER BY category, points_initial, title;

-- name: ListChallengesAdmin :many
SELECT * FROM challenges
WHERE (sqlc.narg('category')::text IS NULL OR category = sqlc.narg('category'))
  AND (sqlc.narg('visible')::boolean IS NULL OR visible = sqlc.narg('visible'))
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'))
ORDER BY category, title
LIMIT $1 OFFSET $2;

-- name: CountChallengesAdmin :one
SELECT count(*) FROM challenges
WHERE (sqlc.narg('category')::text IS NULL OR category = sqlc.narg('category'))
  AND (sqlc.narg('visible')::boolean IS NULL OR visible = sqlc.narg('visible'))
  AND (sqlc.narg('kind')::text IS NULL OR kind = sqlc.narg('kind'));

-- name: CountChallenges :one
SELECT count(*) FROM challenges;

-- name: GetChallengeForUpdate :one
SELECT * FROM challenges WHERE id = $1 FOR UPDATE;

-- name: CreateAttachment :one
INSERT INTO challenge_attachments (id, challenge_id, filename, size_bytes, content_type, object_key)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: GetAttachment :one
SELECT * FROM challenge_attachments WHERE id = $1 AND challenge_id = $2;

-- name: ListAttachments :many
SELECT * FROM challenge_attachments WHERE challenge_id = $1 ORDER BY filename;

-- name: DeleteAttachment :exec
DELETE FROM challenge_attachments WHERE id = $1;
