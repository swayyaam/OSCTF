# Changelog

All notable changes to OSCTF are recorded here. Versions before v1.0 make no API
stability promises (see [`docs/project-desc.md`](docs/project-desc.md)).

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
- License: **TBD** — do not publish the repository until it is decided.
