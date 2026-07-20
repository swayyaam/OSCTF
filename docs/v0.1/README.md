# OSCTF Build Documentation

> Status: **authoritative build spec for v0.1 (MVP)** · Last updated: 2026-07-16
> Parent vision document: [`../project-desc.md`](../project-desc.md)

This directory is the complete, self-contained specification for building the OSCTF platform MVP. It is written for **coding agents and human engineers alike**: every decision is made, every interface is defined, and every milestone has verifiable acceptance criteria. An agent pointed at this directory should be able to build v0.1 without asking product questions.

## How to use these docs (read this first)

1. **The repo root is the directory that contains the `docs/` folder** (these v0.1 docs live in `docs/v0.1/`). All paths in these docs (e.g. `api/`, `dashboard/`, `deploy/`) are relative to that repo root. Scaffold the monorepo alongside `docs/`.
2. **Build in milestone order.** [`12-milestones.md`](12-milestones.md) is the execution plan: M0 → M11, each with tasks and acceptance checks. Do not skip ahead; later milestones assume earlier ones pass their checks.
3. **Two artifacts are the source of truth once they exist:**
   - `api/openapi/openapi.yaml` — the API contract (spec-first; handlers and the TS client are generated from it).
   - `api/internal/db/migrations/` — the database schema.
   These docs specify their *initial* content; once generated code exists in the repo, the repo wins and these docs are updated to match (not the other way around).
4. **When a detail is genuinely unspecified**, apply the Core Principles in [`00-overview.md`](00-overview.md), pick the boring option, and record the decision in the relevant doc in a `## Decision log` section at the bottom. Do not leave TODOs in code.
5. **Never expand scope.** If something feels missing, check the "Out of scope" list in [`00-overview.md`](00-overview.md) — it is probably deferred on purpose.

## Document map

| Doc | Contents |
|---|---|
| [`00-overview.md`](00-overview.md) | What we're building, MVP scope (in/out), principles as build rules, success criteria |
| [`01-architecture.md`](01-architecture.md) | Modular monolith design, package boundaries, core interfaces, key request flows |
| [`02-repo-layout.md`](02-repo-layout.md) | Exact repository tree, Makefile targets, AGENTS.md requirements, conventions |
| [`03-tech-stack.md`](03-tech-stack.md) | Locked library choices with versions, Go & TypeScript coding conventions, lint config |
| [`04-database.md`](04-database.md) | Complete PostgreSQL schema (DDL), indexes, migration policy, seed logic |
| [`05-api.md`](05-api.md) | API conventions, error format, full endpoint reference, WebSocket protocol |
| [`06-auth.md`](06-auth.md) | Registration/login flows, argon2id parameters, sessions, roles, rate limits, CSRF |
| [`07-scoring.md`](07-scoring.md) | Scoring formulas (exact math), solve validity, tiebreaks, scoreboard caching, freeze |
| [`08-challenge-runtime.md`](08-challenge-runtime.md) | ChallengeRuntime interface, Docker implementation, container security defaults, lifecycle |
| [`09-frontend.md`](09-frontend.md) | Route table, page specs, API client codegen, WS client behavior, styling system |
| [`10-deployment.md`](10-deployment.md) | Dockerfile, compose file spec, full env var reference, first-boot behavior |
| [`11-testing-ci.md`](11-testing-ci.md) | Test pyramid, integration test setup, smoke tests, CI pipeline definition |
| [`12-milestones.md`](12-milestones.md) | **The build plan**: M0–M11 with tasks, deliverables, acceptance criteria |
| [`13-example-challenges.md`](13-example-challenges.md) | `challenge.yaml` format and the 8 seeded example challenges, fully specified |

## Suggested kickoff prompt for a coding agent

> Read `docs/v0.1/README.md` and `docs/v0.1/00-overview.md` in full, then skim the remaining docs in `docs/v0.1/` in order. Execute the milestones in `docs/v0.1/12-milestones.md` starting at M0. After each milestone, run its acceptance checks and do not proceed until they pass. Treat `docs/v0.1/` as the spec; when it is ambiguous, follow its instructions for resolving ambiguity.

## Glossary

| Term | Meaning |
|---|---|
| **Event** | A CTF competition instance: a time window during which challenges are open and scored. v0.1 supports exactly one. |
| **Challenge** | A single task with a flag. `standard` kind (description + attachments) or `container` kind (also runs a Docker container). |
| **Flag** | The secret string proving a solve. Static per challenge in v0.1. Default format `OSCTF{...}`. |
| **Solve** | A team's first correct submission for a challenge. |
| **Submission** | Any flag attempt, correct or not. Always logged. |
| **Instance** | A running container for a `container` challenge. v0.1: one shared instance per challenge, per event. |
| **Team** | The scoring unit. Every participant must belong to a team to submit (solo players create a one-person team). |
| **Captain** | The team member who created the team; can rename it and regenerate the invite code. |
| **Hidden** | A user/team excluded from the public scoreboard and from dynamic-scoring solve counts (e.g. admins, testers). |
| **Freeze** | Optional point in time after which the public scoreboard stops updating (admins still see live data). |
