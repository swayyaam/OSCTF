# 00 — Overview & Scope (v0.3)

## What we are building (v0.3)

The version that makes **plugin-first** real. Until now every extensible interface —
`AuthProvider`, `ScoringEngine`, `ChallengeRuntime`, `ObjectStore` — has had exactly one
implementation, compiled into the core. v0.3 adds a **plugin loader** so third parties
register *additional* implementations **out of process**: an OIDC login, a custom scoring
curve, a Discord notifier, a new challenge type — each a standalone executable the core
discovers, launches, supervises, and calls over a stable gRPC ABI. A plugin author edits
nothing in core and opens no PR against it.

Two consequences of "API first" ship alongside, because plugins and agents both need
them:

- **API v1** — the current `/api/v0` surface, promoted to `/api/v1` and declared **stable
  and semver-governed**. This is a clean cut: v0.1 pinned everything at `/api/v0` for
  exactly this moment.
- **A client CLI (`osctf`) and an MCP server** over API v1, plus **API tokens** so
  non-cookie clients (CLI, MCP, integrations, plugins) can authenticate.

## Relationship to v0.1 + v0.2

v0.3 **extends** the shipped code; it does not rewrite it. Concretely:

- The four core interfaces keep their signatures. What changes is that each is looked up
  through a **registry** the loader can extend, instead of being a single injected value.
  The built-in implementations become the default registrations.
- A no-plugin deployment behaves **identically** to v0.2. Plugins are strictly additive.
- `/api/v0` keeps responding — it becomes a deprecated alias for the same handlers now
  also mounted at `/api/v1` — so existing clients and the shipped dashboard keep working
  during the transition.
- The migration is additive (API tokens table, plugin-config table if used); existing
  rows are untouched.

## Principles as build rules (inherited, with v0.3 emphasis)

All seven principles ([`../v0.1/00-overview.md`](../v0.1/00-overview.md)) still apply.
Three get sharper teeth in v0.3:

- **Plugin first.** If a feature the roadmap lists as a plugin can only be built by
  editing core, the *interface* is what needs fixing — not the plugin. Every v0.3 plugin
  type is proven by a first-party reference plugin that lives outside the core packages
  and is loaded exactly the way a third-party plugin would be.
- **API first.** The CLI and the MCP server are **pure clients** of API v1 — they hold no
  business logic and no direct database access. If the CLI can do something the API can't,
  the API is missing an endpoint.
- **AI native.** The CLI is agent-friendly (`--json`, non-interactive flags, meaningful
  exit codes) and the MCP server exposes the API as tools. New in v0.3: the plugin
  template repo and every reference plugin ship their own `AGENTS.md`.

New rule for v0.3 specifically:

- **Plugins are untrusted and isolated.** A plugin runs in its own process; a crash, hang,
  or panic is contained (the core degrades to the built-in default or an error, never
  goes down). Plugin failures are observable. The core never links plugin code in-process.

## MVP scope — IN

| # | Feature | Summary |
|---|---|---|
| G1 | Plugin ABI | A stable gRPC contract (over HashiCorp go-plugin) for the four extensible plugin types. Versioned independently of the HTTP API. |
| G2 | Plugin loader + lifecycle | Discover plugins from a directory + manifest, launch them, health-check, isolate failures, hot-reload on config change, shut down cleanly. |
| G3 | Registries | Auth providers, scoring modes, and challenge types resolve through registries the loader extends; the event bus lets notifiers subscribe. Built-ins are the defaults. |
| G4 | The four plugin types | **Auth** (expanded for redirect/OAuth flows), **Scoring**, **Notification** (new event bus), **Challenge type** (new). Runtime + storage stay in-tree. |
| G5 | First-party reference plugins | OIDC/OAuth auth, one alternative scoring algorithm, a Discord/webhook notifier, and one custom challenge type — each loaded as an external plugin. |
| G6 | API v1 | `/api/v1` declared stable; a stability + deprecation policy; `/api/v0` kept as a deprecated alias. |
| G7 | API tokens | Scoped bearer tokens for non-cookie clients, issued/managed via the API and admin UI. |
| G8 | `osctf` CLI | `init`, `login`, `challenge validate|package|create`, `event`, `instance`, `plugin`, `deploy` — `--json`, non-interactive flags, exit codes. Pure API-v1 client (+ offline authoring). |
| G9 | MCP server | An MCP adapter over API v1 exposing platform operations as agent tools, authenticated by API token. |
| G10 | Plugin author kit | A plugin template repo (Go, with `AGENTS.md`), the plugin SDK helpers, and author docs. |

## MVP scope — OUT (do not build, even if easy)

- **Plugins for the runtime or object store.** Their streaming, SDK-heavy shapes are a
  poor fit for the process boundary, and the Kubernetes runtime is v0.4. They stay in-tree
  in v0.3; the loader is designed so they *could* be added later without an ABI break.
- **WASM plugins / in-process Go `plugin` packages.** The ABI is go-plugin/gRPC only (see
  the fixed decision). Other mechanisms are a post-1.0 open question.
- **A marketplace / plugin registry service, a plugin SDK for non-Go languages, client
  SDKs (JS/Python), a theme system.** All are Phase 5 (v1.0+).
- **Freezing the plugin ABI or API v1 under a no-breaking-changes-ever promise.** v0.3
  *declares* v1 stable and starts semver governance; the hard freeze is v1.0.
- **Kubernetes, multi-event/multi-tenant.** Phase 4 (v0.4/v0.5).

## Success criteria for v0.3

1. A third-party-style plugin (built from the template repo, no changes to core) is
   dropped into the plugins directory, appears in `osctf plugin list`, and is used to log
   a user in / score a challenge / send a notification / define a challenge — end to end.
2. Killing a running plugin process mid-request degrades gracefully: the core returns a
   clear error (or falls back to the built-in default where one exists), stays up, and the
   loader restarts the plugin.
3. Every operation the dashboard performs is reachable through `/api/v1` with an **API
   token** and no session cookie — verified by driving a full event lifecycle from the
   `osctf` CLI.
4. An agent, given only the MCP server and an API token, spins up a practice event with
   selected challenges and reads the scoreboard — no direct DB or shell access.
5. `/api/v0` still answers for the shipped dashboard and the v0.2 smoke/e2e flows; a
   no-plugin deployment passes the entire v0.2 test suite unchanged.
6. The plugin ABI is versioned: a plugin built against a newer/older ABI major is refused
   with a clear message rather than crashing the host.

## Exit criterion (from the roadmap)

> Someone **outside the core team builds and ships a working plugin** without opening a PR
> against core.

Concretely: clone the plugin template repo, implement one plugin type against the SDK,
`osctf plugin package` it, drop it into a running deployment's plugins directory, and have
the platform use it — with zero edits to the `osctf/platform` source tree.

## Fixed product decisions (do not relitigate during build)

| Decision | Value |
|---|---|
| Plugin mechanism | **HashiCorp go-plugin over gRPC** (out-of-process). Not WASM, not the stdlib `plugin` package, not in-process registration. Rationale in [`01-architecture.md`](01-architecture.md). |
| Plugin types in v0.3 | **auth, scoring, notification, challenge-type.** Runtime + object store stay in-tree (extensible later without an ABI break). |
| Plugin discovery | A plugins directory `OSCTF_PLUGINS_DIR` (default `./plugins`), one subdirectory per plugin containing a `plugin.yaml` manifest + the executable. |
| Plugin config | A `config` block per plugin in `plugin.yaml` (schema-declared), overridable by `OSCTF_PLUGIN_<NAME>_*` env. Secrets via env only, never committed. |
| Plugin trust | Plugins are trusted-but-isolated: separate process, supervised, failure-contained. No syscall sandbox in v0.3 (documented limitation; a future hardening item). |
| ABI versioning | The gRPC ABI carries a **major.minor** version in the go-plugin handshake; major mismatch is refused, minor forward-compatible. Independent of the HTTP API version. |
| HTTP API version | **`/api/v1`** is the stable surface. `/api/v0` remains as a deprecated alias (same handlers) through v0.3; removed no earlier than v0.4. |
| API stability policy | Semver from v1: no breaking change to `/api/v1` without a major bump; additive changes are minor; a deprecation carries a `Deprecation`/`Sunset` header and ≥ one minor release of notice. |
| Non-cookie auth | **API tokens** (opaque bearer, hashed at rest, scoped, revocable). Sessions remain for the browser. |
| CLI | A separate client binary **`osctf`** (`api/cmd/osctf`), a pure API-v1 client for remote ops and offline for `init`/`validate`/`package`. The `platform` binary stays the server. |
| MCP transport | `osctf mcp` serves **stdio** MCP (primary) over the API-v1 client, authenticated by API token; SSE/HTTP transport is optional/out-of-scope for v0.3. |
| Event bus | In-process, async, best-effort, at-least-once-not-guaranteed. Core emits typed domain events; notification plugins subscribe by event name. Not durable in v0.3. |
| Codename / module path | Unchanged: `OSCTF`, `github.com/osctf/platform`. |
