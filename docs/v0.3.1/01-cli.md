# 01 — The `osctf` CLI

> Depends on **API v1** and **API tokens**, both shipped in v0.3
> ([`../v0.3/06-api-v1.md`](../v0.3/06-api-v1.md)). The CLI adds no capability the API
> lacks; it is a client of the stable surface v0.3 froze.

`osctf` is the **client** CLI, a new binary at `api/cmd/osctf`, separate from the
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
osctf login --url https://ctf.example.com        # opens browser or prompts; creates + stores an API token
osctf login --url … --token osctf_pat_…          # non-interactive (CI/agents)
osctf whoami [--json]                             # verify auth; prints user + scopes
```

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
  deploy                                     # bring up / migrate a deployment (compose profile helper)
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

The `challenge.yaml` schema + validator already live in the seeder
([`../v0.1/13-example-challenges.md`](../v0.1/13-example-challenges.md),
[`../v0.2/11-example-challenges.md`](../v0.2/11-example-challenges.md)). v0.3 **promotes the
parser/validator to a shared package** (`internal/challengespec` or similar) that both the
seeder and the CLI import, so `osctf challenge validate` gives identical results offline to
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
