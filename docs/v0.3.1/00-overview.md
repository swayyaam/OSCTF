# 00 — Overview & Scope (v0.3.1)

## What we are building (v0.3.1)

**Client tooling for the stable API.** v0.3 promoted the HTTP surface to a stable,
semver-governed **API v1** and added **API tokens** so non-cookie clients can authenticate.
v0.3.1 is the first thing built *on* that promise: the **`osctf` CLI** (G8) and an **MCP
server** (G9), so humans script the platform from a terminal or CI and agents operate it
conversationally. Both are thin clients — they hold no business logic, touch no database,
and can do nothing a caller with the same token could not do in the dashboard.

## Why this is its own version (the split)

v0.3 is the **plugin story**. The CLI and the MCP server are a **separate product surface**:
they depend on API v1 and API tokens, but **not on plugins**, and nothing in the plugin
system depends on them. Shipping them inside v0.3 would put the plugin work behind client
tooling on the same release train and let one slip the other. Splitting them out lets v0.3
ship the plugin loader, ABI, registries, and reference plugins on its own cadence, and lets
this client tooling ship as soon as API v1 is stable.

What deliberately **stayed in v0.3**, not here:

- **API tokens** — plugins and the stable API surface both need them, and success criterion
  3 ("every dashboard operation reachable with a token and no cookie") is a property of the
  **API**, proven against `/api/v1` directly, not of the CLI. Tokens are v0.3.
- **`AGENTS.md` in the plugin template and reference plugins** — that is the AI-native
  property of the *plugin* surface and ships with the plugins in v0.3. The **agent-facing
  CLI and MCP server** — the conversational operation surface — are what move here.

## Relationship to v0.3 (and v0.1 + v0.2)

- **Depends on v0.3's API v1 + API tokens** ([`../v0.3/06-api-v1.md`](../v0.3/06-api-v1.md)).
  The CLI's Go client is generated from the same `openapi.yaml` v0.3 froze at `1.0.0`; the
  MCP tool schemas are generated from the same operations. If a command needs something the
  API can't do, the API is missing an endpoint — fix the API, not the client.
- **Does not depend on plugins.** v0.3.1 builds and ships against a plugin-free deployment;
  plugin admin commands (`osctf plugin list|reload`, the MCP `list_plugins`/`reload_plugin`
  tools) are just more API-v1 calls and degrade cleanly when no plugins are loaded.
- **A v0.3 deployment is complete without v0.3.1.** The dashboard, plugins, and API v1 all
  work with no CLI and no MCP server installed. v0.3.1 is additive tooling.
- **The v0.1 + v0.2 + v0.3 invariants still hold.** This version relitigates none of them.

## Principles as build rules (inherited)

All seven principles ([`../v0.1/00-overview.md`](../v0.1/00-overview.md)) still apply; two
are the reason this version exists:

- **API first.** The CLI and the MCP server are **pure clients** of API v1 — no business
  logic, no direct database access, no privileged path. A CLI action and the equivalent
  dashboard action hit the same endpoint with the same authorization. This is the same rule
  v0.3 states from the server side ("every operation has an API endpoint; the dashboard is
  one client among several"); v0.3.1 is two more of those clients.
- **AI native.** The CLI is agent-friendly by construction: `--json` on every command,
  a non-interactive flag for every prompt, and an exit-code taxonomy mapped to HTTP classes
  so agents and CI branch without parsing prose. The MCP server exposes the API as
  scope-gated tools with destructive actions behind an explicit confirmation.

## MVP scope — IN

| # | Feature | Summary |
|---|---|---|
| G8 | `osctf` CLI | A separate client binary (`api/cmd/osctf`, Cobra): `login`, `whoami`, `context`, `init`, `challenge validate\|package\|create`, `event`, `scoreboard`, `instance`, `submit`, `team`, `user`, `token`, `plugin`, `deploy`, `version`. `--json`, non-interactive flags, exit codes. Pure API-v1 client; offline only for authoring. [`01-cli.md`](01-cli.md) |
| G9 | MCP server | `osctf mcp` — a stdio Model Context Protocol adapter over the API-v1 client, exposing platform operations as agent tools, authenticated by API token, scope-gated, with destructive tools behind `confirm:true`. [`02-mcp.md`](02-mcp.md) |

## MVP scope — OUT (do not build, even if easy)

- **Any capability the API doesn't already expose.** The client tools add ergonomics, never
  new power. If a command wants a capability API v1 lacks, that is an API change (a v0.3.x /
  v0.4 API item), not a client feature.
- **SSE/HTTP MCP transport.** stdio is the only required transport; an HTTP transport is a
  later, optional concern.
- **Client SDKs (JS/Python), a plugin marketplace, GitOps challenge pipelines.** Post-1.0.
- **Re-implementing server operations client-side.** `osctf deploy` orchestrates the
  `platform` binary / compose; it does not run migrations or reimplement the server.

## Success criteria for v0.3.1

1. **Agent-only operation (was v0.3 success criterion 4).** An agent, given only the MCP
   server and an API token — no direct DB or shell access — spins up a practice event with
   selected challenges and reads the scoreboard.
2. **CLI golden path.** `osctf login → whoami → challenge validate → challenge create →
   instance start → submit → scoreboard` succeeds against a running deployment, with the
   documented `--json` shapes and exit codes (`0/2/3/4/5/6/7`).
3. **Offline authoring works with no server.** `osctf challenge validate` returns exit `0`
   on a good `challenge.yaml` and exit `6` on a bad one; `osctf challenge package` produces
   a deterministic tarball — both without network access. The offline validator gives
   results identical to the server (shared `challengespec` parser).
4. **No privileged path.** Everything the CLI or MCP server does is an authenticated,
   audited API-v1 call bounded by the token's scope; neither can exceed what a human with
   that token can do in the dashboard. (The *token-only, no-cookie* property itself is a v0.3
   guarantee, proven against `/api/v1` in [`../v0.3/00-overview.md`](../v0.3/00-overview.md)
   success criterion 3 — the CLI and MCP inherit it by using tokens.)

## Exit criterion for v0.3.1

> An operator drives a full event from the terminal, and an agent drives one through MCP —
> both against a stock deployment, using only API tokens, with no cookie and no DB access.

Concretely: on a running v0.3 deployment, `osctf login` with a token, run the golden path
above end to end; then point an MCP client at `osctf mcp` and complete success criterion 1.
No server-side change is required to make either work — if one is, an API endpoint is
missing and belongs in the API, not the client.

## Fixed product decisions (do not relitigate during build)

| Decision | Value |
|---|---|
| CLI binary | A separate client binary **`osctf`** (`api/cmd/osctf`), Cobra-based. A pure API-v1 client for remote ops; offline only for `init` / `challenge validate` / `challenge package`. The `platform` binary stays the server with its hand-rolled `serve\|migrate\|seed` switch. |
| Remote transport | The **generated Go API-v1 client** (from the same `openapi.yaml` as the TS client). Zero drift from the contract; no privileged backdoor. |
| CLI auth | **API tokens** (v0.3). Named **contexts** in `~/.config/osctf/config.yaml` (like kubectl); precedence `--url/--token` → `OSCTF_URL`/`OSCTF_TOKEN` → current context. Tokens via OS keychain when available, else `0600` config; never printed back. |
| Exit codes | Mapped to HTTP classes: `0` ok · `1` runtime · `2` usage · `3` auth · `4` not-found · `5` conflict · `6` validation · `7` server/plugin unavailable. |
| MCP transport | `osctf mcp` serves **stdio** MCP (primary and only required). SSE/HTTP transport is out of scope. |
| MCP auth + exposure | API token, resolved like the CLI; the token's **scope bounds the tool surface** (a `read` token sees only read tools); destructive tools require `confirm:true`. Generated tool schemas from the OpenAPI operations, so the tool surface can't drift from the API. |
| Codename / module path | Unchanged: `OSCTF`, `github.com/osctf/platform`. |
