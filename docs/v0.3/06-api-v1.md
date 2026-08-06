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
  non-admin user's token grants nothing (`role ∩ scope`). A token carrying an unrecognized
  scope is rejected (fail closed), never treated as scopeless-and-continue.
- **Auth middleware.** `Authorization: Bearer osctf_pat_…` is accepted anywhere a session
  is, resolving to the owning user + scopes. It is read from the Authorization header ONLY —
  never a cookie, query parameter, or form field, since accepting a token from an ambient
  channel would forfeit the CSRF protection bearer requests skip. Sessions remain for the
  browser; a request presents one or the other. Bearer requests are rate-limited by TOKEN
  IDENTITY (not IP), because automation traffic is unlike a browser's.
- **Expiry (decided).** Tokens are NOT immortal. Create takes an optional lifetime; omitted,
  it gets the default (`OSCTF_TOKEN_DEFAULT_TTL`, 90 days); a requested lifetime is capped at
  `OSCTF_TOKEN_MAX_TTL` (365 days) and a longer request is rejected. There is no API path to
  a never-expiring token — a permanent admin-scoped credential is exactly what a security
  product should not mint by default. The create response states the resulting `expires_at`.
- **Lifecycle.** `expires_at` and explicit revoke (`DELETE`) both invalidate on the next
  request. `last_used_at` is best-effort and COARSENED — updated at most once per minute per
  token (a conditional write), so the hot path doesn't amplify writes; it's an "in use
  recently" signal for operators deciding whether to revoke, not an exact timestamp.
- **Ban disables tokens (decided — which mechanism is load-bearing).** The LOAD-BEARING
  mechanism is the per-request live check: every bearer request re-reads the owner's role and
  banned flag, so a ban takes effect on the next request regardless of anything else. Banning
  ALSO deletes the user's token rows (defense-in-depth + row hygiene, the token equivalent of
  session revocation), but that deletion is NOT what makes the ban safe. Do not add a token
  cache on the assumption that "banned tokens are deleted anyway" — that silently reintroduces
  a window equal to the cache TTL. The live check is the guarantee; deletion is a bonus.

### Issuance and revocation are session-only (decided)

**A token cannot issue or revoke tokens.** `createToken`, `revokeToken`, and `listTokens`
require a **session** (cookie) credential; a bearer token is rejected on these routes even
if its owner is otherwise privileged. The reason is containment: if a leaked token could
mint more tokens, revoking the leaked one wouldn't stop the attacker — they'd have already
minted successors that outlive the revocation. Forcing issuance through a session means an
attacker with only a stolen token cannot self-perpetuate; the legitimate owner (or an admin
ban) revokes the token and the attacker is out. Token management is a browser/console
operation, so requiring a session costs the real user nothing. This is enforced in the
policy table (the token-management ops carry an `authn: session` requirement) and asserted
in the matrix like any other authorization rule.

### Revocation is immediate; there is no token-lookup cache (decided)

Revocation latency is a security property, so it is stated explicitly rather than left to
implementation:

- **No caching of token validity.** Every bearer request performs a fresh hashed lookup and
  resolves the owner's **current** role from the users table in the same request. There is
  no in-memory token cache in v0.3. Consequently:
  - A **revoked** or **expired** token fails the **very next** request (there is no window
    where a revoked token still works).
  - A **ban or role demotion** of the owner takes effect on the **next** request — the
    effective permission is `role ∩ scope`, and `role` is read live, so a token never
    outlives its owner's privileges. (This is the same immediacy sessions already have via
    the ban→session-revocation path.)
  - An **in-flight** request that has already passed authentication completes normally;
    revocation stops the *next* request, not one already executing. This is the standard,
    accepted semantics and is not a caching window.
- **If a future version adds a token-lookup cache** for hot-path performance, the cache TTL
  becomes the **maximum revocation latency** and MUST be documented here and bounded (and
  ban/expiry should punch through it). v0.3 deliberately avoids the cache so revocation is
  exact; the hashed-lookup cost is one indexed query per request, acceptable at v0.3 scale.

### Ownership (decided)

- A user **lists and revokes their own** tokens only. `listTokens` returns the caller's
  tokens; `revokeToken` is scoped to the caller (revoking an id that isn't theirs is a 404,
  indistinguishable from a nonexistent id — no cross-user existence leak).
- An **admin lists and revokes anyone's** via the `/admin/tokens` routes.
- **Metadata only, ever.** `listTokens`/`adminListTokens` return `{id, prefix, name, scopes,
  created_at, last_used_at, expires_at}` — never the plaintext (shown once at create) and
  never the `token_hash`. The plaintext exists in exactly one response and nowhere else.

### Endpoints (new, under `/api/v1`)

| Method + path | operationId | Purpose | Auth |
|---|---|---|---|
| `POST /tokens` | `createToken` | Create a token for the caller; returns the secret **once**. | session |
| `GET /tokens` | `listTokens` | List the caller's tokens (metadata only, never the secret). | session |
| `DELETE /tokens/{id}` | `revokeToken` | Revoke one of the caller's tokens. | session |
| `GET /admin/tokens` | `adminListTokens` | Admin view across users (metadata only). | session, admin |
| `DELETE /admin/tokens/{id}` | `adminRevokeToken` | Revoke any user's token. | session, admin |

Every token-management route is **session-only** (a bearer token is rejected on them even if
its owner is privileged — see "Issuance and revocation are session-only").

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
- **Token management is session-only.** A token cannot issue or revoke tokens, so a leaked
  token can't self-perpetuate past its own revocation. (See "Issuance and revocation are
  session-only".)
- **No token-lookup cache; revocation is immediate.** Every bearer request re-looks-up the
  token and resolves the owner's live role, so revocation/ban/demotion take effect on the
  next request. A future cache would make its TTL the max revocation latency and must say so.
  (See "Revocation is immediate".)
- **Stability policy travels with the spec.** Written into `info.description` so any client
  or agent reading the OpenAPI sees the contract.
