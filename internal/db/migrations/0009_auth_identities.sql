-- +goose Up

-- External auth identities: the CORE-MINTED binding between an auth provider's subject and a
-- local user. A row here is the ONLY thing that lets a plugin's claim resolve to an existing
-- account. The core creates it after a login that policy permitted; nothing a plugin returns
-- creates one directly, and no plugin-supplied field is stored as authority. See the auth
-- return-path contract in docs/v0.3/04-plugin-interfaces.md.
--
-- DELIBERATELY NO ROLE COLUMN. Roles are core state, changed only by an admin through the API;
-- no login path writes one. A role here would be a second place an identity could assert
-- authority, which is exactly what the contract forbids. Do NOT add one.
--
-- The PK is (provider, subject): one external identity maps to at most one local user, so a
-- second user can never claim an already-bound subject. A user MAY hold several identities
-- (different providers, or several subjects at one provider) — that is a merge, not a conflict.
CREATE TABLE auth_identities (
    provider   text NOT NULL,
    subject    text NOT NULL,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);

-- The login path looks up (provider, subject), which the PK already covers. This index serves
-- the delete cascade and per-user listing.
CREATE INDEX idx_auth_identities_user ON auth_identities (user_id);

-- +goose Down
DROP TABLE auth_identities;
