# 00 — Overview & Scope

## What we are building (v0.1)

A self-hostable CTF platform where **one person can host a real CTF for ~100 participants on a single server**. The golden path:

```bash
git clone <repo> && docker compose up
```

Within minutes: authentication, team formation, a challenge board with seeded examples, flag submission with scoring, a live scoreboard, and an admin panel — no cloud account, no license key, no external services.

v0.1 is the entry point of a larger vision (see [`../project-desc.md`](../project-desc.md)): an open infrastructure layer for cybersecurity competitions, labs, and training. That vision constrains *how* we build v0.1 (interfaces, API-first, containers) but not *what* — the feature scope below is final for this version.

## Principles as build rules

Every implementation decision gets checked against these. They are restated here as concrete rules, not aspirations:

1. **Self-host first.** Nothing may require an external service. MinIO substitutes for S3, Postgres and Redis run in compose. If a feature needs an internet connection at runtime (other than pulling public Docker images), it does not ship in v0.1.
2. **Open by default.** Everything generated (API clients, DB code) is checked into the repo so a fresh clone builds without running generators. Generation drift is CI-enforced.
3. **Plugin first (interfaces now, loader later).** `AuthProvider`, `ScoringEngine`, `ChallengeRuntime`, and `ObjectStore` are Go interfaces from day one, each with exactly one v0.1 implementation. No plugin loader is built. Code outside these packages must depend on the interface, never the concrete type.
4. **API first.** The web UI calls the same `/api/v0` endpoints available to everyone. No server-rendered shortcuts, no private endpoints. If the UI needs data, the API grows a documented endpoint.
5. **Container first.** Challenge workloads run only in containers, never as host processes. The platform talks to the container runtime exclusively through the `ChallengeRuntime` interface.
6. **Cloud native (later).** v0.1 targets Docker Compose only. Do not write Kubernetes code, but do not write anything that *precludes* it (no host-path assumptions in business logic, no reliance on a single-process global lock for correctness where a DB constraint can do the job).
7. **AI native.** The repo ships `AGENTS.md` (spec in [`02-repo-layout.md`](02-repo-layout.md)). Every Make target is non-interactive. Every error message states what failed and what to check. Docs are deterministic and copy-pasteable.

## MVP scope — IN

Feature-complete list. Each maps to milestones in [`12-milestones.md`](12-milestones.md).

| # | Feature | Summary |
|---|---|---|
| F1 | Accounts | Register (username/email/password), login, logout, change password. No email verification, no self-serve reset (admin resets). |
| F2 | Teams | Create team (creator = captain), join via invite code, leave, rename (captain). Team required to submit. Configurable max size, default 4. |
| F3 | Event window | Exactly one event: name, description, start, end, optional scoreboard freeze. Admin-editable. Enforced server-side. |
| F4 | Challenges | Admin CRUD. Kinds: `standard` (description + attachments + flag) and `container` (adds a Docker image). Markdown descriptions, categories, difficulty labels, visibility toggle, optional max attempts. |
| F5 | Attachments | Upload via admin panel to object storage; participants download streamed through the API. |
| F6 | Flag submission | Rate-limited, logged (every attempt), exact-match static flags (optional case-insensitive), first-correct-per-team counts. |
| F7 | Scoring | `static` and `dynamic` (solve-count decay) per challenge, behind the `ScoringEngine` interface. Exact formulas in [`07-scoring.md`](07-scoring.md). |
| F8 | Scoreboard | Live via WebSocket with polling fallback. Tiebreak by earliest last score-changing solve. Optional freeze. Hidden teams excluded. |
| F9 | Shared challenge instances | Admin deploys one container per `container` challenge via the Docker API: resource limits, health status, logs, stop/restart/destroy. Connection info shown to participants. |
| F10 | Admin panel | Event settings, challenge management (incl. instances), user list (ban/hide/reset password/promote), team list (ban/hide), submission log with filters. |
| F11 | Public pages | Landing page with countdown, challenge board, scoreboard, team pages, user profiles with solve lists. |
| F12 | One-command deploy | `docker compose up` boots everything, migrates, seeds an admin account and the example challenges. |
| F13 | Example challenges | 8 seeded challenges across 6 categories (see [`13-example-challenges.md`](13-example-challenges.md)). |
| F14 | Operational basics | `/healthz`, `/readyz`, Prometheus `/metrics`, structured JSON logs, audit log of admin actions. |
| F15 | Docs & AGENTS.md | Install guide, challenge-authoring guide, event-running guide, repo `AGENTS.md`. |

## MVP scope — OUT (do not build, even if easy)

- Per-team/per-user challenge instances; any instance scheduler beyond the single shared instance
- Plugin loader, plugin discovery, external plugins of any kind
- Kubernetes, Helm, operators, Nomad
- SSO/OAuth/LDAP — email+password only
- Email sending of any kind (verification, reset, notifications)
- Multi-event, multi-tenancy, organizations
- Marketplace, SDKs, themes, CLI beyond the `platform` binary's `serve|migrate|seed` subcommands
- AI features, hint systems, writeup submission
- Announcements/notifications feed
- Flag-sharing detection beyond rate limiting + logged submissions with IPs
- Internationalization (English only)
- Scoreboard charts/graphs (table only; charts are a stretch goal, not scope)

The rule for deferred features: **define the interface, skip the implementation.** Deferral must never force a rewrite.

## Success criteria for v0.1

1. `git clone && docker compose up` on a clean Linux or macOS machine with Docker yields a working platform in under 10 minutes, with zero interactive steps.
2. The smoke test ([`11-testing-ci.md`](11-testing-ci.md)) passes: register → create team → view challenges → submit wrong flag → submit right flag → see scoreboard update.
3. An admin can create a container challenge, deploy its instance, and a participant can reach it — using only the web UI.
4. The platform survives 100 concurrent participants on a 4 vCPU / 8 GB server (excluding challenge container load).
5. A coding agent given the repo and `AGENTS.md` can set up a dev environment and run the test suite without human help.

## Fixed product decisions (do not relitigate during build)

| Decision | Value |
|---|---|
| Project codename | `OSCTF` (placeholder; a rename is a find-replace, so don't over-abstract it) |
| Go module path | `github.com/osctf/platform` (placeholder org) |
| Server binary name | `platform` (subcommands: `serve`, `migrate`, `seed`) |
| API base path | `/api/v0` (unstable API; `v1` arrives in Phase 3) |
| Default flag format | `OSCTF{...}` — convention only; flags are stored verbatim and never validated against the format |
| Default team size limit | 4 (env-configurable) |
| Backend HTTP port | 8080 |
| Challenge host port range | 30000–30999 |
| Timezone handling | Everything UTC; timestamps are RFC 3339 in the API, `timestamptz` in Postgres |
| IDs | UUID v7, exposed as strings |
| License | **Apache-2.0** (decided 2026-07; maximizes adoption + patent grant, matches the Kubernetes-style "become the standard" goal and the plugin/marketplace direction). `LICENSE` + `NOTICE` at the repo root. |
