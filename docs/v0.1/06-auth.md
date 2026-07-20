# 06 — Authentication & Authorization

## Model

Email + password only in v0.1, implemented as `EmailPasswordProvider` behind the `AuthProvider` interface ([`01-architecture.md`](01-architecture.md)). Server-side sessions in Redis, session ID in an HttpOnly cookie. **No JWTs** — sessions must be revocable instantly (bans, password resets), and a single Redis lookup per request is nothing at this scale.

## Registration

`POST /api/v0/auth/register` with `{username, email, password}`:

- `username`: 3–32 chars, `^[a-zA-Z0-9_.-]+$`, unique case-insensitive.
- `email`: syntactically valid (parse with `net/mail`), unique case-insensitive. **No verification email** (no email at all in v0.1) — the email is a login identifier and future contact field only.
- `password`: 8–128 chars, no composition rules (length beats complexity theater). Reject the top-100 common passwords via a small embedded list.
- Open by default; `OSCTF_REGISTRATION_OPEN=false` closes it (admin creates users… by flipping it on temporarily; per-user admin creation is out of scope).
- Success: create user (`role=user`), start a session (auto-login), 201.

## Password hashing

Argon2id via `golang.org/x/crypto/argon2`, stored as a PHC string:

```
$argon2id$v=19$m=65536,t=3,p=4$<b64 salt>$<b64 hash>
```

- Parameters: **memory 64 MiB (`m=65536`), iterations `t=3`, parallelism `p=4`**, salt 16 bytes (`crypto/rand`), key length 32 bytes.
- Parameters live in the hash string → future changes re-hash transparently on next successful login (compare with stored params, re-hash if they differ from current config).
- Verification: decode PHC, recompute, compare with `subtle.ConstantTimeCompare`.

## Login

`POST /api/v0/auth/login` with `{email, password}`:

1. Rate limits first (below) — before any DB hit.
2. Look up by email. **On miss, still run one argon2 hash of the supplied password against a fixed dummy hash** (timing uniformity), then return the generic 401.
3. Banned user → same generic 401 (don't advertise bans at login).
4. On success: rotate — generate a fresh session token, set cookie, 200 with the `/auth/me` payload.

Generic failure body: `title: "Invalid credentials"` — never "wrong password" vs "no such user".

### Login rate limits (Redis sliding window)

| Scope | Limit | Key |
|---|---|---|
| Per IP | 10 attempts / 5 min | `rl:login-ip:{ip}` |
| Per account (by submitted email, hashed) | 5 attempts / 5 min | `rl:login-acct:{sha256(email)}` |

Exceeded → 429 + `Retry-After`. Successful login does not reset windows (cheap, fine).
IP source: `X-Forwarded-For` **only when** `OSCTF_TRUST_PROXY=true` (first untrusted hop), else the socket peer address. Same rule applies to submission logging IPs.

## Sessions

- Token: 32 random bytes (`crypto/rand`), base64url → ~43 chars. Stored in Redis as `sess:{token}` hash: `user_id`, `role`, `created_at`, `ip`, `ua`.
- TTL: **168 h (7 days), sliding** — middleware refreshes TTL when less than half remains (avoids a Redis write per request).
- Role is cached in the session for cheap middleware checks, but **admin endpoints re-read the user row** (promotion/demotion must not wait for session expiry; also catches bans).
- Revocation: logout deletes the key. Ban / admin password-reset / self password-change delete **all** the user's sessions — maintain a set `sess:user:{user_id}` of active tokens to make this O(sessions).
- Cookie: name `osctf_session`, `HttpOnly`, `SameSite=Lax`, `Path=/`, `Secure` when `OSCTF_BASE_URL` is https, `Max-Age` = TTL. No cookie domain attribute (host-only).

## CSRF

Two layers, both required, no token machinery:

1. `SameSite=Lax` cookie — blocks cross-site POSTs from browsers.
2. Middleware on mutating methods: require `Origin` (fallback `Referer`) header whose origin equals `OSCTF_BASE_URL`'s origin; mismatch → 403 `forbidden` with detail `origin check failed`. Requests with neither header are **rejected** — browsers always send Origin for CORS/POST; CLI users must add `-H "Origin: <base-url>"` (document this in the API guide; it's one line and keeps the model simple).

CORS: same-origin deployment (SPA served by the API), so no CORS headers in production. In dev, allow `http://localhost:5173` via env `OSCTF_CORS_DEV_ORIGIN` (empty = disabled).

## Authorization

Two roles. Permission matrix (rows = actions, enforcement in the service layer, *not* just routing):

| Action | anonymous | user (no team) | team member | captain | admin |
|---|---|---|---|---|---|
| View event info, scoreboard, team/user profiles | ✔ | ✔ | ✔ | ✔ | ✔ |
| View challenge list/details (event started) | — | ✔ | ✔ | ✔ | ✔ (also pre-start) |
| Download attachments | — | ✔ | ✔ | ✔ | ✔ |
| Submit flags (event running) | — | — | ✔ | ✔ | ✔* |
| Create/join team | — | ✔ | — | — | ✔ |
| Rename team / regen invite | — | — | — | ✔ | ✔ |
| Leave team | — | — | ✔ | ✔ | ✔ |
| Everything under `/admin/*` | — | — | — | — | ✔ |

\* Admin submissions are allowed for testing but admins must be `hidden=true` (the seed admin is), so they never appear in scoreboards or dynamic solve counts.

Banned team member → submissions rejected 403 (detail `team is banned`). Banned user → sessions already revoked.

## First admin

Created by the seeder on first boot from `OSCTF_ADMIN_EMAIL` / `OSCTF_ADMIN_PASSWORD` (required env), `username=admin`, `role=admin`, `hidden=true`. If a user with that email already exists, the seeder leaves it untouched (idempotent). Log a **warning banner** at boot if the admin password equals the compose-file default.

## Security notes (implement, don't debate)

- Uniform 404 for invisible resources; uniform 401 for auth failures; no user enumeration via register (409 is acceptable there — registration inherently reveals existence; rate-limit it: 5/hour/IP `rl:register-ip`).
- Session token never logged; log `session_id_prefix` (first 8 chars) if correlation is needed.
- All comparisons of secrets (flags, passwords, tokens) constant-time.
- `PATCH /auth/me/password` requires the current password and keeps only the current session alive.
- Response headers on everything: `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, and for the SPA a CSP: `default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:` (Tailwind needs no inline scripts; keep `script-src 'self'`).
