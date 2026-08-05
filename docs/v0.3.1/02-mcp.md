# 02 — MCP Server

> Depends on **API v1** and **API tokens** (v0.3,
> [`../v0.3/06-api-v1.md`](../v0.3/06-api-v1.md)) and on the CLI binary
> ([`01-cli.md`](01-cli.md)), of which it is a subcommand. Like the CLI, it exposes the
> stable API surface and no more.

The MCP server lets an agent operate OSCTF conversationally — "spin up a practice event
with last semester's web challenges and show me the scoreboard." It is a thin **Model
Context Protocol** adapter over the API-v1 Go client: every tool is one (or a small
composition of) API-v1 call(s), authenticated by an **API token**. No business logic, no
DB — the same rule as the CLI.

Ships as `osctf mcp` (a subcommand of the CLI binary), so any MCP client that launches a
stdio command (`command: osctf`, `args: [mcp]`) can register it in one line.

## Transport & auth

- **Transport: stdio** (primary and only required in v0.3). The agent's MCP client launches
  `osctf mcp`; JSON-RPC over stdio. (An optional SSE/HTTP transport is out of scope for
  v0.3.)
- **Auth: API token**, resolved exactly like the CLI (`OSCTF_TOKEN`/`OSCTF_URL` or the
  selected `osctf` context). The token's **scope bounds the tools** the server exposes —
  a `read`-only token surfaces only read tools; `admin` unlocks admin tools. The server
  advertises its resolved scope in the server `instructions`.
- The MCP server inherits the platform's authz: it can never do more than the token's user
  and scope allow. There is no privileged MCP path.

## Tool surface (over API v1)

Tools mirror API operations, grouped and named for agent legibility. Read tools are always
safe; write tools are gated by scope and (destructive ones) by a confirmation argument.

| Tool | Maps to | Scope | Notes |
|---|---|---|---|
| `whoami` | `getMe` | read | Identity + scope self-check. |
| `list_challenges` / `get_challenge` | `listChallenges`/`getChallenge` | read | Board + detail. |
| `get_scoreboard` | `getScoreboard` | read | Standings snapshot. |
| `get_event` / `set_event` | `getEvent`/`adminUpdateEvent` | read / admin | Window + freeze. |
| `create_challenge` / `update_challenge` / `delete_challenge` | admin challenge CRUD | admin | `delete` requires `confirm: true`. |
| `validate_challenge` | `ValidateConfig` + core validation | read | Dry-run authoring check; no writes. |
| `start_instance` / `stop_instance` / `extend_instance` | v0.2 instance ops | submit | Per-team instance control. |
| `list_instances` / `destroy_instance` | admin fleet | admin | `destroy` requires `confirm: true`. |
| `submit_flag` | `submitFlag` | submit | Returns verdict; never reveals other flags. |
| `list_plugins` / `reload_plugin` | plugin admin | admin | Loader state + reload. |
| `list_teams` / `get_team` | teams | read | — |

Each tool's input schema is derived from the OpenAPI operation's parameters/body (generated,
not hand-maintained), so the tool surface tracks the API automatically. Outputs are the
API's JSON, trimmed to what an agent needs, with secrets never included (no flags, no token
values, no password hashes — same guarantees as the HTTP responses).

## Safety rules

- **Read/write separation is explicit.** Tool descriptions state whether a tool mutates
  state; read tools are annotated as safe/idempotent.
- **Destructive tools require an explicit `confirm: true`** argument and echo what will be
  affected — deleting a challenge, destroying an instance, revoking a token. This mirrors
  the CLI's `--yes` gate and gives the agent a clear two-step.
- **Scope-gated exposure.** The server only registers tools the token's scope permits, so an
  agent literally cannot see admin tools with a read token.
- **No shell, no filesystem, no DB.** The MCP server's only capability is calling API v1.
  Anything it can do, a human with the same token can do in the dashboard.
- **Errors are surfaced verbatim** (problem+json → tool error) so an agent can self-correct
  (e.g. a `422` with `field_errors` guides a re-try).

## Example agent flow

> "Set up a practice event tonight with the three web challenges from the examples and give
> me the scoreboard link."

1. `set_event(start, end)` (admin) → window set.
2. `list_challenges` → filter category=web → `update_challenge(visible:true)` for each.
3. Report the dashboard URL + `get_scoreboard` snapshot.

Every step is an audited API-v1 call under the agent's token; nothing bypasses the platform.

## Decision log

- **`osctf mcp`, stdio, over the API client.** One binary, trivial to register with any MCP
  client; the server is a mechanical adapter, not a second brain.
- **Token scope bounds the tool surface.** The safest default: an agent can't even attempt
  what its token can't do.
- **Generated tool schemas from OpenAPI.** The MCP surface can't drift from the API.
- **Confirmation on destructive tools.** Matches the platform's "confirm hard-to-reverse
  actions" posture and keeps agent operation reversible-by-default.
