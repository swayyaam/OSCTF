# OSCTF v0.2 Build Documentation — Dynamic Instances

> Status: **authoritative build spec for v0.2** · Builds on the shipped v0.1 code.
> Parent vision: [`../project-desc.md`](../project-desc.md) · Baseline spec: [`../v0.1/`](../v0.1/README.md)

This directory is the complete, self-contained specification for building OSCTF v0.2.
Unlike v0.1 (which was built from an empty repo), **v0.2 is a delta on the shipped
v0.1 codebase** — it extends the existing packages, migrates the existing schema, and
grows the existing API. Every doc here says what *changes* and what is *added*; it
assumes the v0.1 code exists and passes its tests.

An agent pointed at this directory (plus the v0.1 code and `../v0.1/` for baseline
detail) should be able to build v0.2 without asking product questions.

## The one feature

**Per-team isolated challenge instances.** In v0.1 a `container` challenge has one
shared container that every team hits. In v0.2 a challenge can be **per-team**: each
team starts its own container on demand, with its own port, its own **unique flag**,
network-isolated from other teams, and a **scheduler** that expires it on a TTL and
cleans it up at event end. This is the capability that separates OSCTF from
CTFd-class tools.

## How to use these docs (read this first)

1. **The repo root is the directory containing `docs/`.** All paths are relative to it.
   The v0.1 code lives at `api/`, `dashboard/`, `examples/`, etc. — you are editing it.
2. **Build in milestone order.** [`10-milestones.md`](10-milestones.md) is the execution
   plan: M0 → M7, each with tasks and acceptance checks. Do not skip ahead.
3. **The v0.1 invariants still hold.** Everything in [`../v0.1/00-overview.md`](../v0.1/00-overview.md)
   (principles, the four core interfaces, the API/DB source-of-truth rule, "define the
   interface, skip the implementation") remains in force. v0.2 does not relitigate them.
4. **Backwards compatibility is a requirement.** A v0.1 event (all-`shared` challenges)
   must keep working unchanged after upgrading to v0.2. `instancing` defaults to
   `shared`; `flag_mode` defaults to `static`. The migration is non-destructive.
5. **When a detail is genuinely unspecified**, apply the Core Principles, pick the boring
   option, and record it in a `## Decision log` at the bottom of the relevant doc. No
   TODOs in code.

## Document map

| Doc | Contents |
|---|---|
| [`00-overview.md`](00-overview.md) | Theme, scope (in/out), principles as build rules, success criteria, the exit criterion, fixed decisions |
| [`01-architecture.md`](01-architecture.md) | Instance-ownership model, the new `scheduler` package, changed packages, key flows, what stays absent |
| [`02-database.md`](02-database.md) | Migration `0002`: `instances`/`challenges` changes, new indexes/constraints, Redis keyspace, sqlc |
| [`03-runtime.md`](03-runtime.md) | Extended `ChallengeRuntime`, per-team networks, hardening defaults, dynamic `FLAG` injection, reconcile |
| [`04-scheduler.md`](04-scheduler.md) | The scheduler: spawn-on-demand, TTL expiry, event-end cleanup, per-team quotas, concurrency, metrics |
| [`05-flags.md`](05-flags.md) | Per-instance dynamic flags: generation, storage, injection, submission validation, sharing-detection foundation |
| [`06-api.md`](06-api.md) | New/changed endpoints (participant instance controls, admin observability, changed submit), WS, OpenAPI notes |
| [`07-frontend.md`](07-frontend.md) | Participant instance panel, admin instances page, state handling, testids |
| [`08-deployment.md`](08-deployment.md) | New env vars (TTLs, quotas, isolation), compose changes, sizing |
| [`09-testing-ci.md`](09-testing-ci.md) | New tests (scheduler expiry, per-instance flags, quotas, isolation), the e2e flow, CI additions |
| [`10-milestones.md`](10-milestones.md) | **The build plan**: M0–M7 with tasks, deliverables, acceptance |
| [`11-example-challenges.md`](11-example-challenges.md) | `challenge.yaml` additions and the new/updated example challenges |

## Suggested kickoff prompt for a coding agent

> Read `docs/v0.2/README.md` and `docs/v0.2/00-overview.md` in full, then skim the rest
> of `docs/v0.2/` in order. The v0.1 platform is already built and tagged `v0.1.0`;
> you are extending it. Execute the milestones in `docs/v0.2/10-milestones.md` starting
> at M0, running each milestone's acceptance checks before proceeding. Preserve v0.1
> behaviour for `shared`/`static` challenges. When the spec is ambiguous, follow its
> rule for resolving ambiguity.

## Glossary (additions to the v0.1 glossary)

| Term | Meaning |
|---|---|
| **Instancing** | A challenge's provisioning mode: `shared` (v0.1 — one container for everyone) or `per_team` (one container per team, started on demand). |
| **Owner** | The team that a per-team instance belongs to. Shared instances have no owner (`team_id IS NULL`). v0.2 owns instances by **team**; per-user is deferred. |
| **Flag mode** | `static` (v0.1 — one flag in `challenges.flag`) or `per_instance` (a unique flag generated per team instance, injected as `FLAG`, validated per team). |
| **TTL** | Time-to-live of a per-team instance. The scheduler destroys it at `expires_at`; participants can **extend** it up to a maximum lifetime. |
| **Quota** | The maximum number of concurrent running per-team instances one team may hold across all challenges. |
| **Scheduler** | The in-process component that spawns instances on demand, expires them on TTL, cleans them up at event end, and enforces quotas. |
| **Sharing signal** | A logged event raised when a team submits a flag that matches a *different* team's per-instance flag — the foundation for flag-sharing detection. |
