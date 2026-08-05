# 06 — API v1 & API Tokens

v0.1 pinned everything at `/api/v0` and told clients "unstable until Phase 3." Phase 3 is
now: v0.3 promotes the surface to **`/api/v1`**, declares it **stable and semver-governed**,
and adds **API tokens** so non-browser clients (integrations, plugins, and the
[v0.3.1](../v0.3.1/README.md) CLI + MCP server) can authenticate without a session cookie. `openapi.yaml` stays the single source of truth;
`apigen` + the clients are regenerated and drift-gated.

## The v0 → v1 promotion (a cut, not a break)

- **Same shape.** v1 is v0's surface, cleaned up (below), served under a new prefix. The
  generated strict server is mounted under **both** `/api/v1` (canonical) and `/api/v0`
  (deprecated alias) by the router, so nothing existing breaks.
- **Deprecation.** Every `/api/v0` response carries `Deprecation: true` and a `Sunset`
  header; the OpenAPI marks v0 operations `deprecated: true`. `/api/v0` is removed no
  earlier than v0.4, announced in the CHANGELOG.
- **The dashboard migrates to `/api/v1`** (regenerate its client against the v1 server
  block); the switch is transparent since handlers are shared.

Cleanups folded into v1 (do these once, at the cut — they are the only allowed
"breaking-ish" changes, and only because v0 stays as the alias):

| Cleanup | Rationale |
|---|---|
| Consistent error envelope | RFC 9457 problem+json everywhere with the stable `type` slugs already used; document the full slug list. |
| Consistent pagination | One `{items,total,page,per_page}` page shape across list endpoints. |
| Consistent timestamps | RFC 3339 UTC everywhere. |
| Additive v0.2/v0.3 fields kept | instancing/flag_mode/instance fields, plugin + token endpoints — all additive. |

`openapi.yaml` `info.version` → `1.0.0`; `servers` lists `/api/v1` first and `/api/v0`
(deprecated). The [v0.3.1](../v0.3.1/README.md) CLI + MCP clients target `/api/v1`.

## Stability policy (semver from v1)

The contract that starts now and hard-freezes at v1.0:

- **No breaking change to `/api/v1` without a major bump.** Breaking = removing/renaming an
  endpoint, field, or enum value; tightening a type or validation; changing status-code
  semantics; making an optional request field required.
- **Additive changes are a minor bump** and always safe: new endpoints, new optional
  request fields, new response fields, new enum values in *response-only* positions.
- **Deprecation before removal.** A deprecated element carries `deprecated: true` in the
  spec and (for endpoints) `Deprecation`/`Sunset` headers, and survives ≥ one minor release
  before any major removes it. The CHANGELOG records every deprecation and removal.
- **The version is in the path** (`/api/v1`), not a header — the simplest thing for CLIs,
  MCP, curl, and caches. A future `/api/v2` would be a parallel mount, never an in-place
  break.
- **Generated-code drift gate** keeps the checked-in server/clients honest, as today.

This policy is written into `docs/` and referenced from the OpenAPI `info.description` so it
travels with the contract.

## API tokens (non-cookie auth)

Today the only credential is a Redis-backed session cookie — fine for the browser, useless
for a CLI or an MCP server. v0.3 adds **API tokens**: opaque bearer credentials, hashed at
rest, scoped, and revocable.

### Model (migration `0003`, additive)

```sql
CREATE TABLE api_tokens (
    id          uuid PRIMARY KEY,
    user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        text NOT NULL,                 -- human label ("laptop cli", "ci bot")
    token_hash  text NOT NULL,                 -- sha-256 of the secret; the secret is shown once
    prefix      text NOT NULL,                 -- first 8 chars, for display/lookup
    scopes      text[] NOT NULL DEFAULT '{}',  -- e.g. {read, submit, admin}
    last_used_at timestamptz,
    expires_at  timestamptz,                   -- NULL = no expiry
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_api_tokens_hash UNIQUE (token_hash)
);
```

- **Format.** `osctf_pat_<base32(random 32B)>`. The full value is returned **once** at
  creation; only the hash is stored. Lookups hash the presented token and match
  `token_hash` (constant-time), gated by `prefix` for an index-friendly probe.
- **Scopes.** Coarse in v0.3: `read` (GET), `submit` (participant actions), `admin`
  (admin endpoints). A token cannot exceed its owner's role — an `admin` scope on a
  non-admin user's token grants nothing. Scope is enforced by the same authz middleware
  that checks roles today.
- **Auth middleware.** `Authorization: Bearer osctf_pat_…` is accepted anywhere a session
  is, resolving to the owning user + scopes. Sessions remain for the browser; a request may
  present one or the other, never both required. Bearer requests skip the CSRF origin check
  (there's no ambient cookie to protect) but are rate-limited like sessions.
- **Lifecycle.** `expires_at` and explicit revoke (`DELETE`) both invalidate immediately;
  `last_used_at` is updated best-effort for the admin view. Banning a user disables their
  tokens (same session-revocation path).

### Endpoints (new, under `/api/v1`)

| Method + path | operationId | Purpose |
|---|---|---|
| `POST /tokens` | `createToken` | Create a token for the caller; returns the secret **once**. |
| `GET /tokens` | `listTokens` | List the caller's tokens (prefix + metadata, never the secret). |
| `DELETE /tokens/{id}` | `revokeToken` | Revoke one of the caller's tokens. |
| `GET /admin/tokens` | `adminListTokens` | Admin view across users (metadata only). |

The dashboard gains a "API tokens" section in the profile; the CLI's `osctf login` creates
and stores one ([`../v0.3.1/01-cli.md`](../v0.3.1/01-cli.md)).

## Plugin admin endpoints (new, under `/api/v1`)

Surface the loader state ([`03-plugin-loader.md`](03-plugin-loader.md)) for the admin UI +
CLI:

| Method + path | operationId | Purpose |
|---|---|---|
| `GET /admin/plugins` | `adminListPlugins` | Every loaded plugin: name, type, abi, version, state, last error. Never config secrets. |
| `POST /admin/plugins/{name}/reload` | `adminReloadPlugin` | Re-read manifest + relaunch one plugin. |
| `GET /api/v1/auth/providers` | `listAuthProviders` | Public: available login providers for the login page. |

## Decision log

- **Path versioning + a shared handler set.** Mounting the same server under `/api/v1` and
  `/api/v0` makes the cut zero-risk and the deprecation window trivial to honour.
- **Coarse token scopes in v0.3.** `read`/`submit`/`admin` covers the CLI/MCP needs;
  fine-grained per-endpoint scopes are a post-1.0 refinement. A token never exceeds its
  user's role.
- **Store only the hash; show the secret once.** Standard PAT hygiene; a DB leak can't
  reveal live tokens.
- **Stability policy travels with the spec.** Written into `info.description` so any client
  or agent reading the OpenAPI sees the contract.
