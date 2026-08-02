# Changelog

All notable changes to OSCTF are recorded here. Versions before v1.0 make no API
stability promises (see [`docs/project-desc.md`](docs/project-desc.md)).

## v0.2.1 — Security and reliability hardening

A patch release: security fixes, reliability fixes, and a large test-coverage
expansion. A v0.2.0 event upgrades in place. The only OpenAPI change is the additive,
backward-compatible `unadopted` / `unadopted_networks` fields noted below.

### Security

Each item names the vulnerable behaviour, the impact, and whether operator action is
required on upgrade.

- **Frozen scoreboard leaked through `getTeam` / `getUser`.** Both public routes
  returned live solves (with `solved_at`) and derived points during a freeze, so the
  standings a freeze hides could be reconstructed by polling; the same visibility check
  also failed **open** on a transient Events read error and was unguarded when Events
  was unwired. Fixed to hide post-freeze solves from non-members, fail closed on a
  missing dependency, and serve the last known freeze state on a transient error.
  *Impact:* pre-disclosure of a frozen scoreboard. *Operator action:* none.
- **Unauthenticated WebSocket denial of service.** The public scoreboard socket had no
  connection cap, per-client cap, or handshake rate limit, so one client could open
  connections until the process died. Added admission control (global + per-user/IP
  caps + handshake rate, the global cap clamped to `RLIMIT_NOFILE`). *Impact:* DoS on a
  single-server deployment. *Operator action:* none for defaults; for a large public
  scoreboard raise the process ulimit and `OSCTF_WS_MAX_CONNS`, and behind a reverse
  proxy set `OSCTF_TRUST_PROXY=true`.
- **Instance extend after the event ended.** `Extend` had no phase gate, so a
  participant could push a live instance's TTL past the event during the cleanup
  window and outlive the event. Extend is now rejected outside the running phase.
  *Impact:* instances holding ports/resources past close. *Operator action:* none.
- **Session revocation evadable via index drift.** The `sess:user:{id}` reverse index
  could expire while an actively-used session slid its own TTL forward, so bulk
  revocation (ban/force-logout) could miss a live session. The index is now re-extended
  whenever a session refreshes. *Impact:* a banned user retaining a working session.
  *Operator action:* none (drift self-heals on next request).
- **Per-team bridge reclaimed after upgrade.** A team bridge carrying no resolvable
  `osctf.team_id` (any bridge created under v0.2.0) took the network-GC branch and was
  deleted, including mid-deploy. Such bridges are now flagged unadopted, never
  garbage-collected, and surfaced in the admin fleet view. *Impact:* a live team's
  network torn out from under a running instance after upgrade. *Operator action:*
  after upgrade, review unadopted networks in the fleet view and remove stale bridges
  by hand.
- **Per-instance flags exposed through the admin submissions view.** `submissions.provided`
  stored the raw submitted flag, so a per-team instance flag that was submitted (a
  team's own correct solve, or another team's via sharing) was echoed verbatim in the
  admin submissions view. The write path now redacts real per-instance flags, and
  migration `0004` backfills existing rows. *Impact:* per-team instance flags disclosed
  to admin-panel viewers. *Operator action:* **REQUIRED awareness** — migration 0004
  runs automatically on boot, but **any deployment that ran v0.2 has real per-instance
  flags sitting in `submissions.provided`**, and any such data exported before upgrade
  should be treated as exposed. Backfill is best-effort: a shared flag whose instance
  was already destroyed cannot be matched by value; correct solves are always caught, so
  a team's own flag is never left exposed.
- **Registration blocked for venues on a shared NAT.** Anonymous sign-up was hard-capped
  at 5 per hour per client IP, so a venue registering a hundred-plus players from one NAT
  in the first couple of minutes failed by an order of magnitude. Registration is
  unauthenticated (nothing to key on but the IP), so the limit is now generous by default
  (`OSCTF_REGISTER_IP_BURST=500` per `OSCTF_REGISTER_IP_WINDOW=600s`) and configurable;
  set the burst to 0 to disable, or `OSCTF_REGISTRATION_OPEN=false` to close registration
  for an invite-only event. *Impact:* availability -- legitimate players unable to sign up
  (GitHub issue #1). *Operator action:* none for the default; tune `OSCTF_REGISTER_IP_*`
  for a public-internet deployment.

**Minimum security backport set (for a v0.2 deployment).** The freeze, WebSocket, extend,
session, registration-limit, and per-instance-flag fixes apply cleanly on their own. The
network-GC fix and
the reconcile clock-skew fix ship inside the reconcile-rewrite commit
(`fix(runtime): reconcile against the DB clock…`) and cannot be separated from it, so
backporting either requires that commit (and its fleet-view companion).

### Fixed (reliability)

- **Reconcile no longer no-ops under clock skew.** Grace is evaluated against the
  database clock (`clock_timestamp()`, read in the same pass) instead of the app host's
  clock; a row ahead of the clock is treated as an anomaly (skipped, counted, logged),
  not ignored. A row still recording its container id is never removed regardless of
  age, and a team's bridge is not GC'd while that team has a pending or fresh row.
- **Instance operations no longer serialize behind one slow deploy**, and **leaked host
  ports are reclaimed.** Per-team locks replace the single scheduler mutex; a stale-row
  reaper removes pending/error rows (and their reserved ports) older than
  `OSCTF_INSTANCE_REAP_AFTER`.
- **Background workers are joined on shutdown** with per-pass timeouts, so an in-flight
  teardown is not cut off.
- **WS and REST cannot serve divergent scoreboards.** The hub broadcasts the same
  `apigen.Scoreboard` wire type the REST endpoint returns. WS frames are delivered in
  order (hello before any snapshot; phase never reordered against its snapshot) and
  scoreboard snapshots coalesce per client, bounding a slow client to one pending
  snapshot.

### Added

- **`instances.flag` unique index** (`0003`) so two live instances can never share a
  per-instance flag.
- **Admin fleet view surfaces unresolvable Docker resources.** `AdminInstanceList` gains
  optional `unadopted` and `unadopted_networks` arrays. **Additive, optional,
  backward-compatible OpenAPI change.** New metrics: `osctf_unadopted_containers`,
  `osctf_unadopted_networks`, `osctf_reconcile_actions`, `osctf_reconcile_grace_skipped`,
  `osctf_reconcile_future_rows_total`, `osctf_ws_rejections_total`,
  `osctf_ws_readpump_panics_total`.

### Tests

- Fixed the CI selector and lint build-tags so the integration/dockerint tiers actually
  run (a set of tagged tests, and one file missing its tag, had never executed).
- A 1260-cell authorization matrix (every route × 7 identities × 4 phases, exact status
  per cell), enumeration-safety probes (hidden ≡ nonexistent in status/body/timing), a
  reusable flag-containment scanner (REST/WS/logs/metrics/audit), scoreboard and ws
  white-box unit tests, and reconcile fault injection over a fake runtime.

### Upgrade notes

- **Historical per-instance flag redaction.** Migration `0004` redacts real per-instance
  flags left in `submissions.provided` by v0.2 on boot (see Security above). Best-effort;
  treat any already-exported `submissions.provided` data as exposed.
- **WebSocket file descriptors.** The global WS cap is clamped to a reserved fraction of
  `RLIMIT_NOFILE`. For a large public scoreboard, raise the process ulimit and
  `OSCTF_WS_MAX_CONNS`. See `docs/v0.1/10-deployment.md`.
- Per-team bridges created before this release carry no `osctf.team_id` label; after
  upgrade they are never garbage-collected and appear under `unadopted_networks` in the
  admin fleet view for manual removal once idle. New bridges carry the label and GC
  normally.

### A note on this release's history

This release was assembled from a large branch as grouped, individually-referenceable
commits rather than one squash, so `git log` names each fix. The commits are
self-consistent and readable in isolation, but were **not** each built or tested in
isolation — only the release HEAD is green across every tier. Do not `git bisect`
expecting every intermediate commit to compile.

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
