-- name: CreateAPIToken :one
INSERT INTO api_tokens (id, user_id, name, token_hash, prefix, scopes, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.narg('expires_at'))
RETURNING *;

-- name: GetAPITokensByPrefix :many
-- Auth probe: candidates sharing a presented token's prefix. The caller constant-time
-- compares the full hash; the prefix is only an index hint, never the credential check.
SELECT * FROM api_tokens WHERE prefix = $1;

-- name: ListAPITokensByUser :many
SELECT * FROM api_tokens WHERE user_id = $1 ORDER BY created_at DESC;

-- name: GetAPIToken :one
SELECT * FROM api_tokens WHERE id = $1;

-- name: DeleteAPIToken :execrows
-- Scoped to the owner: a caller can only revoke their own token (0 rows ⇒ not found/not theirs).
DELETE FROM api_tokens WHERE id = $1 AND user_id = $2;

-- name: ListAllAPITokens :many
-- Admin view across users; never selects the hash into any DTO (metadata only at the handler).
SELECT * FROM api_tokens ORDER BY created_at DESC LIMIT $1 OFFSET $2;

-- name: CountAllAPITokens :one
SELECT count(*) FROM api_tokens;

-- name: DeleteAPITokensForUser :exec
-- Ban hook: disable all of a user's tokens, the token equivalent of DeleteAllForUser.
DELETE FROM api_tokens WHERE user_id = $1;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = now() WHERE id = $1;
