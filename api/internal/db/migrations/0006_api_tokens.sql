-- +goose Up

-- API tokens: opaque bearer credentials for non-browser clients (CLI, MCP, CI). The
-- plaintext (osctf_pat_<base32>) is shown once at creation; only its sha-256 hash is stored,
-- so a database leak cannot reveal live tokens. `prefix` is the first chars of the random
-- part, kept for display AND as an index-friendly probe: auth looks the token up by prefix
-- then constant-time-compares the full hash, so the lookup is never a plaintext comparison.
--
-- DELIBERATELY NO CHECK ON `scopes`. The valid scope set (read/submit/admin) is enforced in
-- the application, at create time AND at auth time (an unknown scope fails the request
-- closed). Same reasoning as challenges.type: a DB CHECK would freeze the set behind a
-- schema migration. Do NOT add one here — the write-time + auth-time checks are intentional.
CREATE TABLE api_tokens (
    id           uuid PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         text NOT NULL,
    token_hash   text NOT NULL,
    prefix       text NOT NULL,
    scopes       text[] NOT NULL DEFAULT '{}',
    last_used_at timestamptz,
    expires_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_api_tokens_hash UNIQUE (token_hash)
);

-- Auth probes by prefix (many rows per prefix is astronomically unlikely at 60 bits, but the
-- lookup handles it); listing + ban-cascade go by user.
CREATE INDEX idx_api_tokens_prefix ON api_tokens (prefix);
CREATE INDEX idx_api_tokens_user ON api_tokens (user_id);

-- +goose Down
DROP TABLE api_tokens;
