# 01 — The `osctf` CLI

> Depends on **API v1** and **API tokens**, both shipped in v0.3
> ([`../v0.3/06-api-v1.md`](../v0.3/06-api-v1.md)). The CLI adds no capability the API
> lacks; it is a client of the stable surface v0.3 froze.

`osctf` is the **client** CLI, a new binary at `cmd/osctf`, separate from the
`platform` server binary. It is a pure API-v1 client for anything remote and works offline
only for authoring (`init`, `challenge validate|package`). It holds no business logic and
never touches the database — if it can do something, the API can too. It is built to be
driven by agents: `--json` everywhere, non-interactive flags for every prompt, and
meaningful exit codes.

## Design rules

- **Cobra**-based command tree (the first dependency justifying a CLI framework; the server
  keeps its tiny hand-rolled `serve|migrate|seed` switch). Generated shell completion.
- **Remote calls go through the generated Go API-v1 client** (the same OpenAPI that
  produces the TS client produces a Go client for the CLI). Auth is an **API token**.
- **`--json`** on every command prints a single machine-readable JSON object/array to
  stdout and nothing else; human text goes to stderr. Errors in `--json` mode are a JSON
  object `{ "error": { "type", "title", "detail" } }` on stderr.
- **Non-interactive by default in CI:** every prompt has a flag; `--yes` skips
  confirmations; `OSCTF_TOKEN`/`OSCTF_URL` env cover auth without a config file.
- **Exit codes:** `0` success · `1` generic/runtime error · `2` usage error (bad
  flags/args) · `3` auth required/failed (401/403) · `4` not found (404) · `5` conflict
  (409) · `6` validation failed (422) · `7` server/plugin unavailable (5xx). Agents branch
  on these.

## Config & auth

- Config file `~/.config/osctf/config.yaml` holds named **contexts** (like kubectl):
  `{ url, token-ref }`. `osctf login` writes one; `--context` / `OSCTF_CONTEXT` selects it.
- Precedence: `--url/--token` flags → `OSCTF_URL`/`OSCTF_TOKEN` env → current context.
- Tokens are stored via the OS keychain when available, else the config file with `0600`
  and a loud note; never printed back.

```
osctf login --url https://ctf.example.com        # prompts for email + password; mints and stores a token
osctf login --url … --token osctf_pat_…          # non-interactive (CI/agents): store an existing token
osctf whoami [--json]                             # verify auth; prints user + scopes
```

### How `login` gets a token (and what it cannot do)

Minting a token is **session-authenticated by design** — a token cannot mint another token
([`../v0.3/06-api-v1.md`](../v0.3/06-api-v1.md)), and there is no device-code or CLI-OAuth
endpoint. So interactive `login` is a **bootstrap**: prompt for email + password → `POST
/auth/login` → hold the resulting session **in memory only** → `POST /tokens` → store the
token → discard the session (call `POST /auth/logout`). The cookie never reaches disk and the
password is never stored.

Two consequences, stated rather than discovered:

- **Interactive login does not work on an SSO-only deployment** (`OSCTF_AUTH_EMAIL_LOGIN=false`).
  A CLI cannot complete a redirect login without an endpoint built for it. On such a
  deployment, create the token in the dashboard and use `osctf login --token …`. The CLI must
  say exactly that when `GET /auth/providers` shows no credential provider, rather than
  failing with a bare 403.
- **This is the one place the CLI touches a cookie.** Every other command is token-only. If
  that becomes unacceptable — or SSO-only deployments need first-class CLI auth — the fix is a
  device-authorization endpoint in the API, not client-side cleverness.

## Command tree

```
osctf
  login / logout / whoami / context (list|use)
  init                         # scaffold an event/challenge workspace (offline)
  challenge
    validate <dir>             # offline: parse + validate challenge.yaml (+ plugin type via API if reachable)
    package  <dir> -o out.tar  # offline: build the challenge bundle (yaml + files [+ built image ref])
    create   <dir>             # remote: create the challenge from a dir/bundle
    list / get <slug> / rm <slug>
  event   get | set --start … --end … --freeze …
  scoreboard [--top N]
  instance start|stop|extend <slug>          # per-team instance ops (v0.2)
             admin list | destroy <id>       # admin fleet
  submit <slug> <flag>
  team    create|list|get
  user    list|get                           # admin
  token   create --name … --scope …|list|revoke <id>
  plugin  list | reload <name>               # admin: loader state
  mcp                                         # run the MCP server (see 02-mcp.md)
  version
```

## Representative commands

```bash
# Author flow (offline where possible)
osctf init challenge web-login              # scaffolds challenge.yaml + src/ + AGENTS.md
osctf challenge validate ./web-login --json # -> {ok:true} or {ok:false, field_errors:{…}}, exit 6 on invalid
osctf challenge package  ./web-login -o web-login.tar
osctf challenge create   ./web-login        # remote; needs 'admin' token scope

# Run an event from the terminal / CI
osctf event set --start 2026-09-01T00:00:00Z --end 2026-09-02T00:00:00Z --freeze 2026-09-01T22:00:00Z
osctf challenge list --json | jq '.[].slug'
osctf scoreboard --top 10 --json

# Participant / agent
osctf instance start web-login --json       # {state, host_port, connection_info, expires_at}
osctf submit web-login 'osctf{…}'           # exit 0 correct, 5 already-solved, 6/… otherwise

# Operate plugins
osctf plugin list --json                    # loader state incl. health + last error
osctf plugin reload oidc
```

## `validate` / `package` — the authoring reuse

The `challenge.yaml` schema + validator live in the seeder today
(`internal/seed/challenge_yaml.go`; see
[`../v0.1/13-example-challenges.md`](../v0.1/13-example-challenges.md),
[`../v0.2/11-example-challenges.md`](../v0.2/11-example-challenges.md)). **M0 promotes the
parser/validator to a shared package** (`internal/challengespec`) that both the seeder and the
CLI import — a pure refactor with no behaviour change. (An earlier draft said v0.3 did this; it
did not, and the work is assigned here.), so `osctf challenge validate` gives identical results offline to
what the server accepts. When a challenge references a **plugin challenge-type**, `validate`
calls the API's `ValidateConfig` if a context is configured, else validates only the core
fields and notes the skipped type-check. `package` produces a deterministic tarball (yaml +
attachments + optional built image reference) suitable for `create` or a future registry.

## What the CLI is NOT

- Not a second implementation of anything: no direct DB, no bypass of authz, no privileged
  path. A CLI action and the equivalent dashboard action hit the same endpoint with the
  same authorization.
- Not the server: it never runs migrations against a DB directly; `osctf deploy` orchestrates
  the `platform` binary / compose, it doesn't reimplement them.

## Decision log

- **Separate `osctf` binary, Cobra.** Clean server/client split (matches the roadmap's org
  structure where `cli` is its own repo); the server binary stays dependency-light.
- **Generated Go API client, token auth.** Zero drift from the contract; no privileged
  backdoor.
- **Exit-code taxonomy mapped to HTTP classes.** Lets agents and CI branch without parsing
  prose.
- **Share the challenge-spec parser with the seeder.** One validator, identical offline and
  server-side results.
- **`Me.scopes` was added to the API so a client can see its own limits** (2026-08-25). The
  spec had `whoami` printing scopes and the MCP server gating tools on them, and the API
  exposed neither — a client could only learn what its token could do by trying something and
  being refused. This is the response these docs already prescribe for the case: a client
  needing what the API lacks means the API is missing something. The field is additive and
  appears only for token auth, since a session carries the account's full role and an empty
  list there would read as "no permissions" rather than "not applicable".
- **Interactive `login` is a password→token bootstrap, not a browser flow** (decided
  2026-08-24). Token creation is session-only on purpose, and no CLI-auth endpoint exists, so
  the alternatives were this, adding a device-authorization endpoint, or dropping interactive
  login entirely. The bootstrap needs no API change and covers the common case; its limit — no
  SSO-only support — is documented above rather than papered over, and the endpoint remains the
  right fix if that limit starts to bite.
- **`osctf deploy` is NOT in v0.3.1** (decided 2026-08-24). It was a one-line sketch — "compose
  profile helper" — with no flags, behaviour, or acceptance, while every other command has a
  defined shape. Shipping a vague command is worse than not shipping one; it returns when
  someone can say what it does.
