# OSCTF v0.3.1 Build Documentation — Client Tooling (CLI + MCP)

> Status: **authoritative build spec for v0.3.1** · Builds on v0.3's stable API v1 + tokens.
> Parent vision: [`../project-desc.md`](../project-desc.md) · Baselines: [`../v0.1/`](../v0.1/README.md), [`../v0.2/`](../v0.2/README.md), [`../v0.3/`](../v0.3/README.md)

This directory is the complete, self-contained specification for building OSCTF v0.3.1:
the **`osctf` CLI** and the **MCP server**. It is a **delta on v0.3** — it consumes the
stable API v1 and API tokens that v0.3 shipped, and adds two clients of them. It changes
no server behaviour.

An agent pointed at this directory (plus the shipped code and `../v0.3/06-api-v1.md` for
the API-v1 + token detail) should be able to build v0.3.1 without asking product questions.

## The one theme

**The stable API deserves first-class clients.** v0.3 froze the HTTP surface at API v1 and
added API tokens so non-cookie callers can authenticate. v0.3.1 delivers the first two
clients of that surface: a terminal/CI **CLI** and a conversational **MCP server** for
agents. Both are pure clients — no business logic, no database, no privileged path — so they
prove, rather than assume, that the API is complete: anything the dashboard can do, a token
holder can do from a script or an agent.

Why separate from v0.3: v0.3 is the *plugin* story, and the CLI/MCP are an independent
product surface that depends on API v1 but not on plugins. See
[`00-overview.md`](00-overview.md) for the full rationale.

## How to use these docs (read this first)

1. **The repo root is the directory containing `docs/`.** Paths are relative to it. The
   shipped server + dashboard live at `api/`, `dashboard/`; the CLI is new at
   `api/cmd/osctf`.
2. **Build in milestone order.** [`04-milestones.md`](04-milestones.md) is the execution
   plan: M0 → M2.
3. **v0.3 must already be built.** This version assumes API v1 is mounted and stable, the
   `api_tokens` table exists, Bearer auth works, and the OpenAPI is at `1.0.0`. If any of
   that is missing, finish v0.3 first.
4. **The client adds no capability.** Every command and every tool is one (or a small
   composition of) API-v1 call(s). When a command seems to need something the API lacks,
   that is a missing API endpoint — record it as an API change, do not add logic to the
   client.
5. **When a detail is genuinely unspecified**, apply the Core Principles, pick the boring
   option, and record it in a `## Decision log` at the bottom of the relevant doc. No TODOs
   in code.

## Document map

| Doc | Contents |
|---|---|
| [`00-overview.md`](00-overview.md) | Scope, the split rationale, dependency on v0.3, principles, success + exit criteria, fixed decisions |
| [`01-cli.md`](01-cli.md) | The `osctf` client CLI: command tree, `--json`, exit codes, offline vs remote, config/auth |
| [`02-mcp.md`](02-mcp.md) | The MCP server: stdio transport, the tool surface over API v1, scope-gating, safety |
| [`03-testing-ci.md`](03-testing-ci.md) | The `cli` CI job (golden path + offline + MCP), and how it fits the existing pipeline |
| [`04-milestones.md`](04-milestones.md) | **The build plan**: M0–M2 with tasks, deliverables, acceptance |

## Suggested kickoff prompt for a coding agent

> Read `docs/v0.3.1/README.md` and `docs/v0.3.1/00-overview.md` in full, then the rest of
> `docs/v0.3.1/` in order. The OSCTF platform is built through `v0.3` — API v1 is stable,
> API tokens work. You are adding two clients: the `osctf` CLI and its `mcp` subcommand.
> Execute the milestones in `docs/v0.3.1/04-milestones.md` starting at M0, running each
> milestone's acceptance checks before proceeding. The clients add no server capability;
> if a command needs one, it is a missing API endpoint, not a client feature.

## Glossary (additions to the v0.1 + v0.2 + v0.3 glossaries)

| Term | Meaning |
|---|---|
| **`osctf`** | The client CLI (distinct from the `platform` server binary); speaks API v1, works offline for authoring. |
| **Context** | A named `{ url, token }` target in the CLI config, selectable per-command (like a kubectl context). |
| **MCP server** | A Model Context Protocol adapter (`osctf mcp`, stdio) over API v1, exposing platform operations as scope-gated agent tools. |
| **Tool (MCP)** | One agent-callable operation, generated from an OpenAPI operation; read tools are safe, destructive tools require `confirm:true`. |
