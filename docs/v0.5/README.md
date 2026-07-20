# v0.5 — Multi-Event (Scale, part 2)

> Status: **planned stub — not yet build-ready.** Scope below is inherited from the roadmap in [`../project-desc.md`](../project-desc.md#L184). Do not start building from this file; first expand it into full topic docs following the [`../v0.1/`](../v0.1/README.md) template.

## Theme

**One deployment, many events.** The second Phase 4 (Scale) release, building on the Kubernetes runtime from [`../v0.4/`](../v0.4/README.md).

## Scope (from roadmap)

- **Multiple concurrent events** on one deployment.
- **Org/tenant boundaries** separating who administers and participates in what.
- **Per-event isolation** of challenges, teams, and scoreboards.

## Exit criterion

Combined with v0.4: one deployment serves a 1,000+ participant event on Kubernetes while **a second, smaller event runs concurrently** — with no code differences from the single-server path.

## Builds on v0.1

The v0.1 schema and services were built single-event but **not single-event-only** — this is the deliberate seam to widen:

- [`../v0.1/04-database.md`](../v0.1/04-database.md) — the `events` table already supports many rows; v0.1 "enforces a single event row in the service layer (first row wins)" and notes "the schema supports many for later phases." Most core tables (challenges, teams, submissions) will need an `event_id` foreign key and the queries scoped by it — design this migration and its backfill carefully.
- [`../v0.1/05-api.md`](../v0.1/05-api.md) — the participant API assumes one implicit event (`GET /event`); multi-event needs event scoping in the path or context.
- [`../v0.1/06-auth.md`](../v0.1/06-auth.md) — two global roles (user/admin) become per-tenant/per-event roles.

## To make this build-ready

Write the numbered topic docs: the tenancy/event data model and migration, the scoping strategy for every query and endpoint, the revised authorization model, per-event scoreboard/cache keying, admin UX for managing multiple events, and a milestone plan with acceptance checks — same depth as `../v0.1/`.
