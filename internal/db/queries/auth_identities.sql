-- name: GetAuthIdentity :one
SELECT * FROM auth_identities WHERE provider = $1 AND subject = $2;

-- name: CreateAuthIdentity :one
INSERT INTO auth_identities (provider, subject, user_id)
VALUES ($1, $2, $3)
RETURNING *;
