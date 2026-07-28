# 00 — Overview & Scope (v0.2)

## What we are building (v0.2)

The feature that separates OSCTF from CTFd-class tools: **per-team isolated challenge
instances with a scheduler.** A challenge author can mark a `container` challenge as
`per_team`; then each team clicks **Start** to get its own container — its own host
port, its own unique flag, network-isolated from other teams — and the **scheduler**
handles the whole lifecycle (spawn on demand, expire on a TTL, extend, clean up at
event end, enforce per-team quotas). Organizers never watch `docker ps`.

v0.2 also does the **runtime hardening pass** that v0.1 explicitly deferred
(read-only rootfs + tmpfs, egress control, per-team network isolation) and lays the
**flag-sharing-detection foundation** (per-instance flags let the platform tell whose
flag was submitted).

## Relationship to v0.1

v0.2 **extends** the shipped v0.1 code; it does not rewrite it. The v0.1 architecture
was designed for exactly this — `ChallengeRuntime` already takes an `InstanceSpec` and
the `instances` table already has per-instance rows. Concretely:

- The four core interfaces (`AuthProvider`, `ScoringEngine`, `ChallengeRuntime`,
  `ObjectStore`) keep their v0.1 signatures where possible; `ChallengeRuntime` grows a
  minimal, additive change (owner in the spec — see [`03-runtime.md`](03-runtime.md)).
- The migration is **non-destructive and additive** (new nullable columns, relaxed
  constraints). Existing rows keep working.
- A v0.1 event — every challenge `shared`/`static` — behaves identically after the
  upgrade. Per-team is strictly opt-in per challenge.

## Principles as build rules (inherited, with v0.2 emphasis)

All seven v0.1 principles ([`../v0.1/00-overview.md`](../v0.1/00-overview.md#L15)) still
apply. Two get sharper teeth in v0.2:

- **Container first → isolation first.** Per-team instances are hostile-multi-tenant.
  Every instance runs read-only-rootfs, no-new-privileges, all caps dropped, pid/mem/cpu
  limited, on a **per-team network**, with egress off unless the challenge opts in. The
  socket-mount caveat from v0.1 is unchanged and re-stated.
- **Plugin first (interfaces now).** The scheduler talks to the runtime **only** through
  `ChallengeRuntime`. It does not import the Docker SDK. When a Kubernetes runtime lands
  in v0.4, the scheduler is unaffected.

New rule for v0.2 specifically:

- **The scheduler owns instance lifecycle; handlers never do.** Handlers validate a
  request and call the scheduler; the scheduler is the single writer of instance state
  transitions and the single caller of the runtime for lifecycle ops. This keeps
  spawn/expire/cleanup race-free and centralizes quota/TTL logic.

## MVP scope — IN

| # | Feature | Summary |
|---|---|---|
| G1 | Per-team instancing | `challenges.instancing = shared \| per_team`. `per_team` container challenges are started per team, on demand. |
| G2 | Scheduler | Spawn-on-demand, TTL expiry, extend, event-end cleanup, per-team concurrent-instance quota. In-process, tick-driven, race-free. |
| G3 | Per-instance dynamic flags | `challenges.flag_mode = static \| per_instance`. `per_instance` generates a unique flag per team instance, injects it as `FLAG`, and validates submissions per team. |
| G4 | Runtime hardening | Read-only rootfs + `/tmp` tmpfs, `no-new-privileges`, cap-drop ALL (kept), per-instance resource limits (kept), **per-team network isolation**, opt-in egress restriction (`--internal`). |
| G5 | Participant instance controls | On a `per_team` challenge: **Start**, **Stop**, **Extend**, live connection info, and a countdown to expiry — in the challenge detail UI. |
| G6 | Instance observability | Admin **Instances** page: every instance (shared + per-team) with owner, state, port, age, expiry, and health; destroy any instance. `osctf_instances{state}` already exists; add per-owner counts. |
| G7 | Flag-sharing signal | On submit, if the provided flag matches a *different* team's per-instance flag, record a **sharing signal** (audit row + metric). Detection only; no automated scoring action in v0.2. |
| G8 | Example challenges | At least two example challenges exercising `per_team` + `per_instance` (one web, one pwn) plus a hardening demo. |

## MVP scope — OUT (do not build, even if easy)

- **Per-user instances.** Ownership is by **team** only in v0.2. (`instances.team_id`;
  a per-user model is a future column, not built now.)
- **A real job queue / distributed scheduler.** The scheduler is in-process and
  tick-driven, like v0.1's background workers. Kubernetes/operator scheduling is v0.4.
- **Automated anti-cheat action.** v0.2 *detects and surfaces* flag sharing; it does not
  auto-ban, auto-zero, or auto-flag teams. Organizers act on the signal manually.
- **Manual point adjustments**, marketplace, plugins loader, SSO, multi-event — all
  remain deferred to their roadmap phases.
- **gVisor/Firecracker/rootless Docker.** The hardening pass is within the Docker runtime
  (read-only rootfs, tmpfs, per-team nets, egress). Stronger sandboxes remain a future
  open question; document honestly, do not build.
- **Snapshot/restore or migration of running instances.** Instances are cattle: destroyed
  and re-spawned, never migrated.

## Success criteria for v0.2

1. A `per_team` container challenge: two teams each click Start, each gets a distinct
   container on a distinct port with a **distinct flag**; team A cannot solve with team
   B's flag; the scoreboard credits each correctly.
2. The scheduler expires an instance at its TTL (verified with an injected clock) and
   destroys **all** per-team instances at event end — with no operator action.
3. A team hitting its instance quota gets a clear `409`; stopping an instance frees the
   slot.
4. Per-team network isolation holds: team A's container cannot reach team B's container's
   internal port over the Docker network (verified in a build-tagged runtime test).
5. Read-only-rootfs + tmpfs defaults do not break the example challenges; a challenge can
   declare extra writable paths.
6. A v0.1-style event (all `shared`/`static`) upgraded to v0.2 behaves identically — the
   v0.1 smoke test and e2e flows still pass unchanged.
7. Submitting another team's per-instance flag raises a logged sharing signal visible in
   the admin submission log.

## Exit criterion (from the roadmap)

> An event with pwn/web challenges where **every team gets its own instances**, and the
> scheduler — not an operator watching `docker ps` — handles the full lifecycle.

Concretely: run a real (or realistic) event with at least one `per_team` web and one
`per_team` pwn challenge, ≥ 2 teams, TTL expiry and quotas active, start-to-finish, with
zero manual `docker` intervention, and fold the feedback into the v0.3 plan.

## Fixed product decisions (do not relitigate during build)

| Decision | Value |
|---|---|
| Instance owner | **Team** (`instances.team_id`). Per-user deferred. Shared instances have `team_id IS NULL`. |
| Instancing default | `shared` (v0.1 behaviour). `per_team` is opt-in per challenge. |
| Flag mode default | `static`. `per_instance` is opt-in and only valid for `container` challenges. |
| Instance TTL default | `OSCTF_INSTANCE_TTL` = **3600 s** (1 h); per-challenge override `instance_ttl_seconds`. `0` / null = no TTL (event-end cleanup only). |
| Extend | Adds `OSCTF_INSTANCE_EXTEND` = **1800 s** per call, capped at `OSCTF_INSTANCE_MAX_TTL` = **14400 s** (4 h) total lifetime. |
| Per-team quota | `OSCTF_TEAM_INSTANCE_QUOTA` = **3** concurrent running per-team instances across all challenges. |
| Network model | One Docker bridge network **per team** (`osctf-team-<short-id>`), created on demand, removed when the team's last instance is destroyed. Shared instances stay on the v0.1 `osctf-challenges` network. |
| Egress | On by default; a challenge may set `egress: false` → its per-team network is created `--internal`. |
| Rootfs | Read-only by default; `/tmp` mounted `tmpfs` (64 MiB, `noexec,nosuid`). Extra writable dirs via `writable_paths` in `challenge.yaml`. |
| Who starts a per-team instance | The **team** (any member), from the challenge detail UI, during the `running` phase. Admins may also start/stop/destroy any instance from the admin UI. |
| Migration | `0002_dynamic_instances.sql`, additive + non-destructive, with a tested down migration. |
| API version | Still `/api/v0` (unstable until Phase 3). New endpoints are added under it. |
| Codename / module path | Unchanged: `OSCTF`, `github.com/osctf/platform`. |
