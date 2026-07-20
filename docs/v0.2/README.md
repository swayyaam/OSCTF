# v0.2 — Dynamic Instances

> Status: **planned stub — not yet build-ready.** Scope below is inherited from the roadmap in [`../project-desc.md`](../project-desc.md#L184). Do not start building from this file; first expand it into full topic docs following the [`../v0.1/`](../v0.1/README.md) template.

## Theme

**The feature that separates the platform from CTFd-class tools:** per-team isolated challenge instances.

## Scope (from roadmap)

- Per-team (and per-user) instance provisioning through the existing `ChallengeRuntime` interface — the interface was designed in v0.1 for exactly this, so this should extend, not replace, it.
- The **scheduler**: spawn on demand, expire on TTL, cleanup on event end, per-team instance quotas.
- Resource limits per instance (CPU, memory, pids) and **network isolation** between team instances.
- Participant instance controls in the UI: start, stop, extend, connection info.
- **Per-instance dynamic flags** (each team gets a unique flag) — the foundation for flag-sharing detection.
- Instance observability: organizers see what's running, what's stuck, and what it costs.
- **Hardening pass** on the runtime informed by the isolation-depth open question (seccomp profiles, no-new-privileges, read-only rootfs defaults, egress restrictions) — the v0.1 runtime doc lists these as explicitly deferred here.

## Exit criterion

An event with pwn/web challenges where **every team gets its own instances**, and the scheduler — not an operator watching `docker ps` — handles the full lifecycle.

## Builds on v0.1

The v0.1 spec deliberately left seams for this work. When expanding this stub, review these v0.1 docs first — they name what was deferred to here:

- [`../v0.1/08-challenge-runtime.md`](../v0.1/08-challenge-runtime.md) — shared-instance-only; hardening, read-only rootfs, egress restriction, and TTL cleanup are all marked "v0.2".
- [`../v0.1/04-database.md`](../v0.1/04-database.md) — `instances` has `uq_instances_challenge` (one shared instance); per-team instances require relaxing this to `(challenge_id, team_id)` and adding TTL/owner columns. This is a schema migration to design carefully.
- [`../v0.1/07-scoring.md`](../v0.1/07-scoring.md) — "Manual point adjustments" and dynamic-flag-based anti-cheat are noted as revisited here.

## To make this build-ready

Write the numbered topic docs this version needs (at minimum: an updated runtime spec, the scheduler design, the instance-lifecycle state machine, the network-isolation model, the dynamic-flag mechanism, schema migrations, new API surface, UI changes, and a milestone plan with acceptance checks) — same depth and style as `../v0.1/`.
