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
-- Admin view across users; the handler maps to metadata only (never the hash).
SELECT sqlc.embed(t), u.id AS user_id, u.username
FROM api_tokens t JOIN users u ON u.id = t.user_id
ORDER BY t.created_at DESC LIMIT $1 OFFSET $2;

-- name: GetAPITokenAdmin :one
-- Admin revoke path: fetch by id (any owner) to confirm existence before deleting.
SELECT * FROM api_tokens WHERE id = $1;

-- name: DeleteAPITokenByID :execrows
-- Admin revoke: delete any token by id regardless of owner.
DELETE FROM api_tokens WHERE id = $1;

-- name: CountAllAPITokens :one
SELECT count(*) FROM api_tokens;

-- name: DeleteAPITokensForUser :exec
-- Ban hook: disable all of a user's tokens, the token equivalent of DeleteAllForUser.
DELETE FROM api_tokens WHERE user_id = $1;

-- name: TouchAPIToken :exec
-- COARSENED: only writes if last_used_at is stale by > 1 minute, so a hot token doesn't
-- amplify writes (row churn / WAL / vacuum) on every request. last_used_at is therefore an
-- "in use recently" signal, not an exact timestamp — enough for an operator deciding whether
-- a token is still live before revoking it.
UPDATE api_tokens SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');
