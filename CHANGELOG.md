# Changelog

All notable changes to OSCTF are recorded here. Versions before v1.0 make no API
stability promises (see [`docs/project-desc.md`](docs/project-desc.md)).

## v0.2.0 — Dynamic per-team instances

The feature that sets OSCTF apart from CTFd-class tools: **per-team isolated
challenge instances with an in-process scheduler.** Mark a `container` challenge
`per_team` and each team clicks **Start** to get its own container — its own host
port, its own unique flag, network-isolated from other teams — while the scheduler
handles the whole lifecycle. A v0.1 event (all `shared`/`static`) upgrades in place
and behaves identically.

### Added

- **Per-team instancing** — `challenges.instancing = shared | per_team`. A
  per-team challenge is started on demand per team; the participant challenge view
  gains a Start / Stop / Extend panel with connection info and a live TTL countdown.
- **Scheduler** — an in-process, tick-driven component that spawns instances on
  demand, enforces a per-team concurrent quota, expires them on a TTL, lets teams
  extend up to a maximum lifetime, and tears down all per-team instances at event
  end. Shared instances are left for post-event practice.
- **Per-instance dynamic flags** — `challenges.flag_mode = static | per_instance`.
  A per-instance challenge mints a unique `osctf{…}` flag per team instance,
  injects it as `FLAG`, and validates submissions against the submitting team's own
  instance. A team can never solve with another team's flag.
- **Flag-sharing signal** — submitting a flag that matches a *different* team's
  per-instance flag records a `flag.shared` audit entry and a metric. Detection
  only; never revealed to the submitter and never logged with the flag value.
- **Runtime hardening** (the v0.1-deferred pass) — every deployed container now
  runs read-only-rootfs with `/tmp` (and declared `writable_paths`) as tmpfs, on a
  **per-team Docker network**, with egress off (`--internal`) when the challenge
  opts out. `no-new-privileges`, cap-drop ALL, and resource limits as before.
- **Admin instances page** — a fleet view of every instance (shared + per-team)
  with owner, state, port, network, age, expiry, and health, plus destroy-by-id.
- **Example challenges** — `per-team-web` and `per-team-pwn` (per_team +
  per_instance) and `hardening-demo` (a read-only-rootfs showcase).
- **Config** — `OSCTF_INSTANCE_TTL` / `_EXTEND` / `_MAX_TTL`,
  `OSCTF_TEAM_INSTANCE_QUOTA`, `OSCTF_FLAG_PREFIX`; the challenge host-port range
  widened to `30000–32767`.

### Changed

- Migration `0002_dynamic_instances` is additive and non-destructive: new nullable
  columns, owner-aware partial unique indexes replacing the one-instance-per-
  challenge constraint, and a wider host-port range. Existing rows are unchanged.
- The API stays `/api/v0`. New endpoints (`POST/DELETE /challenges/{slug}/instance`,
  `/instance/extend`, `GET /admin/instances`, `DELETE /admin/instances/{id}`) are
  additive; `getChallenge` gains `instancing`, `flag_mode`, and the caller's
  instance; challenge authoring gains the new fields (rejected for non-container).

### Security

- Per-instance flags are secrets from birth: never serialized in any API response
  (participant or admin), never logged, and never placed in audit metadata or
  metric labels — enforced by leak-scan tests.
- Per-team network isolation (internal networks) is verified with a real
  cross-bridge connection probe in the runtime integration tests.

## v0.1.0 — MVP

The first release: **one person can host a real CTF for ~100 participants on a
single server.** `git clone && docker compose up` yields authentication, teams, a
challenge board with seeded examples, flag submission with scoring, a live
scoreboard, and an admin panel — no cloud account, no license key.

### Added

- **Accounts & sessions** — email/password registration and login (argon2id,
  Redis-backed revocable sessions), logout, self-serve password change. Login
  timing uniformity, sliding-window rate limits, and a CSRF origin check.
- **Teams** — create (captain), join by invite code, leave (captaincy transfers),
  rename, invite-code regeneration; one team per user; configurable max size.
- **Event** — a single event window (start/end + optional scoreboard freeze),
  enforced server-side, with pre/running/ended phases.
- **Challenges** — admin CRUD for `standard` and `container` kinds; markdown
  descriptions, categories, difficulties, visibility, optional max attempts;
  attachments in object storage (MinIO). Participant board and detail with
  visibility + phase gating; the flag is structurally absent from participant
  responses.
- **Scoring** — `static` and `dynamic` (solve-count decay) engines as pure
  functions behind a `ScoringEngine` interface.
- **Submissions** — rate-limited, fully logged (every attempt, with IP),
  constant-time flag comparison, first-correct-per-team, atomic double-solve
  prevention.
- **Scoreboard** — computed from ground truth, Redis-cached, with optional
  freeze; live over a throttled WebSocket with a polling fallback.
- **Shared challenge instances** — one Docker container per `container`
  challenge via the `ChallengeRuntime` interface (`DockerRuntime`): resource
  limits, `no-new-privileges`, dropped capabilities, flag injection, health,
  logs, reconcile; deploy/restart/destroy from the admin panel.
- **Admin panel** — dashboard stats, event settings, challenge editor (incl. the
  instance panel and attachments), user/team management (ban/hide/role/reset),
  and a filterable submission log with auto-refresh.
- **Web dashboard** — React 19 + TypeScript SPA (dark/light themes), embedded in
  the Go binary and served same-origin.
- **Operations** — `/healthz`, `/readyz`, Prometheus `/metrics`, structured JSON
  logs with request IDs, an audit log of admin actions, and a one-command
  compose deployment that migrates and seeds on first boot.
- **Examples** — 8 seeded challenges across 6 categories (`make examples` builds
  the 4 container images), doubling as the `challenge.yaml` reference.
- **Docs** — install, challenge-authoring, and event-running guides;
  `AGENTS.md`; the full v0.1 build spec under `docs/v0.1/`.

### Security notes

- The platform mounts the host Docker socket to run challenge containers — this
  is **root-equivalent on the host**. Run events on a dedicated VM. Shared Docker
  isolation is unsuitable for kernel-level or destructive pwn challenges (per-team
  instances and stronger isolation land in v0.2).
- Dependency audit at release: `golang.org/x/text` and `docker/docker` bumped to
  patched versions. Remaining advisories are not exploitable in this deployment:
  the `docker/docker` CVEs with no upstream fix sit behind the already-dominant
  socket-mount risk; the React Router advisory affects RSC/framework mode, which
  this client-side SPA does not use; the `js-yaml` advisory is in build-time
  tooling only. Build the production image with a current Go toolchain to pick up
  `crypto/tls` fixes.

### Known limitations (deferred to later versions)

- Per-team/per-user instances and a scheduler (v0.2).
- Clearing a nullable challenge field via `PATCH` (send a value or leave it out).
- Manual scoreboard point adjustments (documented workaround in the event guide).
- Registration is rate-limited to 5/hour per IP; events behind a shared NAT
  should set `OSCTF_TRUST_PROXY` or pre-register.

### License

Released under the [Apache License 2.0](LICENSE).
