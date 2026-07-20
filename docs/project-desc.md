# Project Vision — Open Security Competition & Training Platform

> Working title: **OSCTF** (placeholder — naming decided later)
> Status: planning · Last updated: 2026-07-07

## One-liner

An open-source, self-hostable platform for cybersecurity competitions, hands-on labs, workshops, and security training — deployable anywhere with a single command.

## Positioning

Deliberately **not** pitched as "an open-source CTF hosting platform." That's what it *is* at v0.1, but not what it *becomes*. The pitch is:

> **An open platform for cybersecurity competitions, labs, and training infrastructure.**

CTFs are the entry point; the durable value is being the infrastructure layer that universities, communities, and companies build their security education on.

## Mission

Most cybersecurity communities, universities, and organizations struggle to run CTFs because existing platforms are difficult to deploy, difficult to customize, or limited in scope. This project aims to become the **open infrastructure layer for cybersecurity education** — a modern, extensible platform that anyone can self-host, extend, and contribute to.

It should make deployment, management, and participation straightforward whether the event is a university CTF, a security workshop, a classroom lab, an internal company training, or a national competition.

---

# Core Principles

Every architectural decision gets checked against these.

## 1. Self-host first

Everything runs locally. The golden path is:

```bash
git clone <repo> && docker compose up
```

Within minutes the user has authentication, a dashboard, challenge deployment, a scoreboard, and an admin panel running. No cloud account, no license key, no external services required.

## 2. Open by default

Every component is documented, versioned, extensible, and replaceable.

## 3. Plugin first

Nothing should require modifying the core. Authentication providers, scoring algorithms, notifications, analytics, themes, and challenge types are all implemented as plugins against stable interfaces. If a feature can't be built as a plugin, the plugin API is what needs fixing.

## 4. API first

Every action has an API. The web UI is just one client of it — which means CLIs, bots, integrations, and custom frontends get full capability for free.

## 5. Container first

Every deployable challenge is isolated in its own container. The platform abstracts over the runtime — Docker, Kubernetes, Podman, and eventually Firecracker — so challenge authors package once and organizers deploy anywhere.

## 6. Cloud native

The same application code runs on Docker Compose (laptop, small event), Kubernetes (large event, university infrastructure), and potentially Nomad later. Scaling is a deployment concern, not an application rewrite.

## 7. AI native

The platform is designed to be operated *with* AI agents, not just by humans reading docs:

- **Every repo ships an `AGENTS.md`** describing setup, architecture, conventions, and common tasks — so a user can clone the repo, open Claude Code / Cursor / Copilot, and say "set this up for me" and the agent has everything it needs.
- **Docs are written for two readers**: humans and agents. Setup guides are step-by-step, deterministic, and copy-pasteable; errors are descriptive enough for an agent to self-correct.
- **The CLI is agent-friendly**: structured output (`--json`), non-interactive flags for every prompt, meaningful exit codes.
- **An MCP server exposes the platform API** so agents can manage events, author challenges, and debug deployments conversationally ("spin up a practice event with the web challenges from last semester").

The API-first principle makes this nearly free: if every action has an API, every action is agent-automatable.

---

# Who It Serves

| Audience | What they get |
|---|---|
| Participants | Clean UI, fast scoreboard, reliable challenge instances, practice mode, personal statistics |
| Organizers | One-command deployment, event management, analytics, anti-cheat, monitoring, scaling |
| Challenge authors | Git-based development, Docker packaging, local testing, CI validation, marketplace publishing |
| Universities | Semester labs, practical assignments, workshops, graded exercises |
| Companies | Internal security training, hiring challenges, team-building events, certifications |

The first three are the MVP audience. Universities and companies are what the plugin/API architecture enables later.

---

# High-Level Architecture

```
                          Internet
                              │
                     Reverse Proxy / CDN
                              │
                        API Gateway
                              │
        ┌─────────────────────┼─────────────────────┐
        │                     │                     │
     Frontend            Authentication      Event Service
                                                  │
                    ┌───────────────┬──────────────┘
                    │               │
              Challenge        Team Service
               Service
                    │
              Deployment Service
                    │
              Docker / Kubernetes
                    │
           Challenge Containers
                    │
     PostgreSQL ─ Redis ─ Object Storage
                    │
            Monitoring & Analytics
```

Note: the diagram shows logical services. At MVP they live in a single backend process (a modular monolith); they only split into separate deployables when scale demands it. The service boundaries exist in the code from day one so the split is possible without a rewrite.

## Components

**Web dashboard** — everything users interact with: login, scoreboard, challenges, teams, profile, analytics.

**Backend API** — the brain: authentication, teams, flag submission, events, admin operations, notifications.

**Challenge runtime** — starts and destroys challenge containers, enforces resource limits, manages networking and isolation.

**Scheduler** — instance lifecycle: expiration, cleanup, per-team instance limits.

**Workers** — background tasks: emails, backups, analytics aggregation, log processing.

**Monitoring** — logs, metrics, traces, alerts. Ships with sensible defaults so organizers see problems before participants do.

---

# Proposed Tech Stack

Criteria: single-binary-friendly for easy self-hosting, first-class Docker/Kubernetes client libraries, strong typing, and boring/durable choices over trendy ones.

| Layer | Proposal | Rationale |
|---|---|---|
| Backend | **Go** | Best-in-class Docker/K8s SDKs, compiles to one static binary, the entire cloud-native ecosystem (Docker, K8s, Prometheus) is written in it. Plugin story via gRPC/HashiCorp go-plugin or WASM. |
| Frontend | **React + TypeScript** (Vite) | Largest contributor pool, shared types with the API via OpenAPI codegen. |
| API style | **REST (OpenAPI-first)** + WebSockets for live scoreboard | OpenAPI spec is the contract; server stubs, TS client, and docs all generate from it. |
| Database | **PostgreSQL** | The default answer. Handles relational data, JSONB for flexible challenge metadata. |
| Cache / queue | **Redis** | Scoreboard caching, rate limiting, background job queue (start with Redis-backed jobs, not Kafka). |
| Object storage | **S3-compatible** (MinIO bundled for self-host) | Challenge attachments, backups, exports. |
| Challenge runtime | **Docker API first**, Kubernetes operator later | Compose-based self-hosting is the golden path; the runtime interface is abstracted so a K8s backend slots in at Phase 3. |
| Observability | **Prometheus + Grafana** (optional compose profile) | Standard, self-hostable, zero licensing. |

Alternatives considered: TypeScript/Node backend (faster iteration, weaker container-orchestration story), Python/FastAPI (great for the AI features later, but worse deployment ergonomics for self-hosters). Decision isn't final until the MVP spec is — but Go is the working assumption.

---

# MVP Scope (v0.1)

The smallest thing that is genuinely useful: **one person can host a real CTF for ~100 participants on a single server.**

## In

- User registration/login (email + password), teams
- Challenge CRUD via admin panel: static challenges (description + attachments + static flag)
- Dynamic scoring (decay by solve count) and static scoring
- Flag submission with rate limiting
- Live scoreboard
- Admin panel: event window (start/end), users, teams, submissions
- **Containerized challenges: per-event shared instances** (one container per challenge, exposed on a port) via the Docker API
- `docker compose up` deployment with seeded example challenges
- Basic docs: install, author a challenge, run an event
- `AGENTS.md` at the repo root: setup, architecture map, conventions, common tasks — maintained as a first-class artifact, not an afterthought

## Out (deliberately deferred)

- Per-team/per-user challenge instances and the scheduler
- Plugin system (design the interfaces, don't build the loader)
- Kubernetes operator, Helm charts
- Marketplace, SDK, themes
- SSO/OAuth (email auth only at v0.1)
- AI features
- Multi-event / multi-tenancy

The one architectural rule for MVP: even though features are deferred, the **interfaces** (auth provider, scoring engine, challenge runtime) are defined as internal abstractions from day one, so deferral never becomes a rewrite.

---

# Roadmap

Each phase has a theme, a scope, and an explicit exit criterion — a phase isn't done when its features merge, it's done when the exit criterion is demonstrated. Versions before v1.0 make no API stability promises; v1.0 is where the stability contract starts.

## Phase 0 — Spec & skeleton (v0.0.x)

**Theme: an empty platform that boots.** No features, all decisions.

- OpenAPI spec for the core API (auth, events, challenges, teams, submissions) — written first, code generated from it
- Database schema + migration tooling
- Monorepo scaffolding: `api/ dashboard/ runtime/ docs/ examples/ deploy/`
- CI: lint, test, build, OpenAPI validation, TS client codegen
- The compose file: backend, frontend, PostgreSQL, Redis, MinIO all start and healthcheck green
- Internal interfaces defined (auth provider, scoring engine, challenge runtime) even though each has exactly one implementation
- `AGENTS.md` skeleton at the repo root

**Exit criterion:** `git clone && docker compose up` serves a login page backed by a live API, on a clean machine, with zero manual steps.

## Phase 1 — MVP (v0.1)

**Theme: one person can host a real CTF for ~100 participants on a single server.** Everything in the MVP scope above:

- Email/password auth, user profiles, team creation/join
- Challenge CRUD via admin panel: static challenges (description, attachments, static flag)
- Static and dynamic (solve-count decay) scoring behind the scoring-engine interface
- Flag submission with rate limiting and full submission logging
- Live scoreboard (WebSocket-pushed)
- Admin panel: event window, user/team management, submission review
- Containerized challenges as per-event shared instances via the Docker API
- Seeded example challenges covering each supported type
- Docs: install, author a challenge, run an event — deterministic and agent-followable
- `AGENTS.md` maintained as a first-class artifact

**Exit criterion:** a real event with a friendly community runs start-to-finish on v0.1 with no manual database surgery, and the feedback is collected into the v0.2 plan.

## Phase 2 — Dynamic instances (v0.2)

**Theme: the feature that separates the platform from CTFd-class tools.** Per-team isolated challenge instances:

- Per-team (and per-user) instance provisioning through the challenge runtime interface
- The scheduler: spawn on demand, expire on TTL, cleanup on event end, per-team instance quotas
- Resource limits per instance (CPU, memory, pids) and network isolation between team instances
- Participant instance controls in the UI: start, stop, extend, connection info
- Per-instance dynamic flags (each team gets a unique flag) — the foundation for flag-sharing detection
- Instance observability: organizers see what's running, what's stuck, and what it costs
- Hardening pass on the runtime informed by the isolation-depth open question (seccomp profiles, no-new-privileges, read-only rootfs defaults)

**Exit criterion:** an event with pwn/web challenges where every team gets its own instances, and the scheduler — not an operator watching `docker ps` — handles the full lifecycle.

## Phase 3 — Extensibility (v0.3)

**Theme: nothing new requires touching core.** The plugin-first principle becomes real:

- Plugin loader + lifecycle (discover, load, configure, isolate failures) over the interfaces defined since Phase 0
- First-party plugins that prove each interface: OAuth/SSO auth, an alternative scoring algorithm, Discord/webhook notifications, one custom challenge type
- The `platform` CLI: `init`, `create challenge`, `validate`, `deploy`, `package` — structured output (`--json`), non-interactive flags, meaningful exit codes
- **Public API v1 declared stable**: versioned, documented, semver-governed from here on
- **MCP server** over API v1 so agents can manage events and author challenges conversationally
- Plugin author docs + a plugin template repo (with its own `AGENTS.md`)

**Exit criterion:** someone outside the core team builds and ships a working plugin without opening a PR against core.

## Phase 4 — Scale (v0.4–v0.x)

**Theme: the same code, from laptop to cluster.** Likely two releases:

- **v0.4 — Kubernetes runtime:** a K8s backend for the challenge runtime interface, the operator (challenge/instance CRDs), Helm charts, horizontal scaling of the API behind a load balancer, externalized state (managed Postgres/Redis support)
- **v0.5 — Multi-event:** multiple concurrent events on one deployment, org/tenant boundaries, per-event isolation of challenges, teams, and scoreboards

**Exit criterion:** one deployment serves a 1,000+ participant event on Kubernetes while a second, smaller event runs concurrently — with no code differences from the compose-based single-server path.

## Phase 5 — Ecosystem (v1.0+)

**Theme: other people's work runs on the platform.** v1.0 is a stability promise, not a feature dump:

- **v1.0:** API v1 + plugin API frozen under semver; migration guides from CTFd (import path decision from the open questions lands here at the latest); production hardening, backup/restore, upgrade path guarantees
- **Post-1.0, driven by RFCs:** marketplace for challenges/plugins/themes, plugin SDK, client SDKs (JS/Python), theme system, GitOps challenge pipeline (a challenge is a Git repo: CI validates, publishes, platform deploys), AI features (hints, difficulty estimation, cheat detection)

**Exit criterion:** the marketplace has more community-authored content than first-party content, and a breaking change hasn't shipped since v1.0.

---

# Long-Term Features

Kept here as direction, not commitments.

- **AI** — hint generation, challenge generation, difficulty estimation, cheat detection, solution verification.
- **Marketplace** — community publishes challenges, plugins, themes, and deployment templates.
- **SDK** — developers build plugins without touching core.
- **CLI** — `platform init / create challenge / validate / deploy / package / publish`.
- **GitOps** — a challenge is a Git repo: CI validates it, publishes it, and the platform deploys it. Challenge development gets the same workflow as software development.

---

# Repository Strategy

**MVP: one monorepo.** Backend, frontend, docs, example challenges, and the compose file live together. Splitting repos before there are contributors just multiplies maintenance.

```
<org>/platform          # monorepo: api/ dashboard/ runtime/ docs/ examples/ deploy/
```

**Long-term: a GitHub organization**, split out as components stabilize and gain independent contributors. Every repo in the org carries its own `AGENTS.md` (and templates include one pre-filled), so any repo — a plugin, a challenge, a theme — is agent-ready from the first clone:

```
<org>/
├── platform            # backend API
├── dashboard           # frontend
├── runtime             # challenge deployment engine
├── operator            # Kubernetes operator
├── cli                 # official CLI
├── sdk                 # plugin SDK
├── api-types           # shared types (OpenAPI-generated)
├── client-sdk-js / client-sdk-python
├── docs / examples / templates
├── challenges          # official challenge collection (crypto, web, pwn, rev, forensics, osint, misc, blockchain, ai)
├── plugins             # official plugins (discord, github, oauth, ldap, smtp, prometheus, k8s, docker)
├── helm-charts / docker-compose / terraform / ansible
└── roadmap / rfcs / governance / awesome-platform
```

Community process borrows from Rust/Kubernetes/React: every major feature starts as an RFC, governance is documented, and the roadmap is public.

---

# North Star

> **Build the Kubernetes of cybersecurity training infrastructure.**

Not because it should be as complex as Kubernetes, but because it should become the **standard foundation** that universities, communities, companies, and organizers build upon. The same platform and APIs should serve someone running a workshop on a laptop and a national competition on a cluster with thousands of participants.

That is a larger and more durable vision than "an open-source CTF platform," and it gives every architectural decision a direction.

---

# Open Questions

- **Name.** Needs to be decided before the repo goes public; check trademark/domain availability.
- **License.** AGPL protects against closed-source SaaS forks but scares some companies; Apache-2.0 maximizes adoption. Decide before first release.
- **Instance isolation depth.** Are Docker containers enough for hostile pwn challenges, or is gVisor/Firecracker needed earlier than Phase 4? (Kernel-exploit challenges can escape plain containers.)
- **How CTFd-compatible to be.** Supporting CTFd's challenge format as an import path would lower switching costs enormously — worth it, or does it constrain the design?
- **Anti-cheat scope for MVP.** Flag-sharing detection is hard; is submission rate limiting + IP logging enough for v0.1?
- **First real event.** Which community/university event can serve as the v0.1 pilot?
