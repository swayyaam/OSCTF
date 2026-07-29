# OSCTF v0.3 Build Documentation — Extensibility

> Status: **authoritative build spec for v0.3** · Builds on the shipped v0.1 + v0.2 code.
> Parent vision: [`../project-desc.md`](../project-desc.md) · Baselines: [`../v0.1/`](../v0.1/README.md), [`../v0.2/`](../v0.2/README.md)

This directory is the complete, self-contained specification for building OSCTF v0.3.
Like v0.2, **v0.3 is a delta on the shipped codebase** — it extends the existing
packages and API rather than rewriting them. Every doc says what *changes* and what is
*added*; it assumes the v0.2 code exists and passes its tests.

An agent pointed at this directory (plus the shipped code and `../v0.1/` + `../v0.2/`
for baseline detail) should be able to build v0.3 without asking product questions.

## The one theme

**Nothing new requires touching core.** v0.1 defined four interfaces with exactly one
implementation each; v0.2 proved the runtime interface with a second dimension (per-team
instances). v0.3 makes the **plugin-first** principle real: a plugin **loader** lets
third parties register *additional* implementations of the extensible interfaces —
authentication, scoring, notifications, and challenge types — **out of process**, so a
crashing or malicious plugin cannot take down the core, and a plugin author never edits,
recompiles, or opens a PR against core.

Two more headline deliverables ride along, both direct consequences of "API first":

- **API v1, declared stable** — `/api/v1`, semver-governed from here on. (v0.1 pinned
  everything at `/api/v0` precisely so this is a clean promotion, not a break.)
- **A client CLI (`osctf`) and an MCP server** over API v1 — so humans script the
  platform and agents operate it conversationally.

## How to use these docs (read this first)

1. **The repo root is the directory containing `docs/`.** All paths are relative to it.
   The shipped code lives at `api/`, `dashboard/`, `examples/` — you are extending it.
2. **Build in milestone order.** [`10-milestones.md`](10-milestones.md) is the execution
   plan: M0 → M7, each with tasks and acceptance checks. Do not skip ahead.
3. **The v0.1 + v0.2 invariants still hold.** Everything in
   [`../v0.1/00-overview.md`](../v0.1/00-overview.md) and
   [`../v0.2/00-overview.md`](../v0.2/00-overview.md) (principles, the four core
   interfaces, the API/DB source-of-truth rule, "define the interface, skip the
   implementation") remains in force. v0.3 does not relitigate them.
4. **Backwards compatibility is a requirement.** A v0.2 deployment with no plugins must
   keep working unchanged; the built-in email/password auth, the static/dynamic scoring
   engines, and the Docker runtime remain first-class and are simply the *default*
   registrations. `/api/v0` keeps responding (deprecated alias) for one release.
5. **When a detail is genuinely unspecified**, apply the Core Principles, pick the boring
   option, and record it in a `## Decision log` at the bottom of the relevant doc. No
   TODOs in code.

## Document map

| Doc | Contents |
|---|---|
| [`00-overview.md`](00-overview.md) | Theme, scope (in/out), principles as build rules, success criteria, exit criterion, fixed decisions |
| [`01-architecture.md`](01-architecture.md) | Host/plugin split, the plugin-mechanism decision, where plugins hook, new packages |
| [`02-plugin-abi.md`](02-plugin-abi.md) | The go-plugin/gRPC ABI: handshake, the protobuf service per plugin type, ABI versioning |
| [`03-plugin-loader.md`](03-plugin-loader.md) | Discovery, the `plugin.yaml` manifest, lifecycle (load/configure/health/isolate/reload), config, trust model |
| [`04-plugin-interfaces.md`](04-plugin-interfaces.md) | The four extensible surfaces in detail — auth, scoring, notifications, challenge types — and the internal event bus |
| [`05-first-party-plugins.md`](05-first-party-plugins.md) | Reference plugins that prove each interface: OIDC/OAuth, an alt scoring algorithm, a Discord/webhook notifier, a custom challenge type |
| [`06-api-v1.md`](06-api-v1.md) | API v1 stability policy, semver + deprecation, the v0→v1 diff, and API tokens (non-cookie auth) |
| [`07-cli.md`](07-cli.md) | The `osctf` client CLI: command reference, `--json`, exit codes, offline vs remote, config/auth |
| [`08-mcp.md`](08-mcp.md) | The MCP server: transport, the tool surface over API v1, auth, and safety |
| [`09-testing-ci.md`](09-testing-ci.md) | The plugin contract-test harness, CLI + MCP tests, and CI additions |
| [`10-milestones.md`](10-milestones.md) | **The build plan**: M0–M7 with tasks, deliverables, acceptance |
| [`11-plugin-template.md`](11-plugin-template.md) | The plugin template repo, author docs, and its `AGENTS.md` |

## Suggested kickoff prompt for a coding agent

> Read `docs/v0.3/README.md` and `docs/v0.3/00-overview.md` in full, then skim the rest
> of `docs/v0.3/` in order. The OSCTF platform is already built and tagged `v0.2.0`; you
> are extending it. Execute the milestones in `docs/v0.3/10-milestones.md` starting at M0,
> running each milestone's acceptance checks before proceeding. Preserve v0.1/v0.2
> behaviour: a no-plugin deployment must work unchanged, and `/api/v0` must keep
> responding. When the spec is ambiguous, follow its rule for resolving ambiguity.

## Glossary (additions to the v0.1 + v0.2 glossaries)

| Term | Meaning |
|---|---|
| **Plugin** | An out-of-process executable that implements one of the extensible interfaces (auth, scoring, notification, challenge type) and is loaded by the core at boot. |
| **Plugin ABI** | The stable gRPC contract between the core (host) and a plugin, carried over HashiCorp go-plugin. Versioned independently of the HTTP API. |
| **Host** | The core `platform` process that discovers, launches, supervises, and calls plugins. |
| **Registry** | The in-core table mapping a name (auth provider id, scoring mode, challenge type, …) to an implementation — built-in *or* plugin-provided. |
| **Manifest** | A plugin's `plugin.yaml`: its name, type, ABI version, executable, and config schema. |
| **API v1** | The first stability-promised HTTP surface (`/api/v1`); semver-governed from v0.3 on. |
| **API token** | A bearer credential (personal/service access token) for non-cookie clients — the CLI, the MCP server, and integrations. |
| **Event bus** | The in-core publisher of domain events (`challenge.solved`, `event.started`, …) that notification plugins subscribe to. |
| **`osctf`** | The client CLI (distinct from the `platform` server binary); speaks API v1 and works offline for authoring. |
| **MCP server** | A Model Context Protocol adapter over API v1, exposing platform operations as agent tools. |
