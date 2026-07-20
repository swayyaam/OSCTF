# 12 — Milestones (Build Plan)

Execute in order. Each milestone lists **tasks**, **deliverables**, and **acceptance** — commands that must succeed (run from the repo root) before moving on. Where a milestone touches docs (`AGENTS.md`, guides), that update is part of the milestone, not an afterthought.

Dependencies are linear except where noted (M8/M9 can interleave with M7).

---

## M0 — Scaffold & tooling

**Tasks**: repo tree per [`02-repo-layout.md`](02-repo-layout.md); Go module + `main.go` with `serve|migrate|seed` subcommand switch (serve = hello-world HTTP on :8080 with `/healthz`); Vite dashboard scaffold rendering a placeholder page; Makefile with all targets (later ones may no-op with a message); `.golangci.yml`, ESLint/Prettier, `.env.example`; compose files with postgres/redis/minio + healthchecks; CI with `api-lint`, `api-test`, `web` jobs green; initial `AGENTS.md` (setup + map).

**Acceptance**:
```
make setup && make lint && make test        # green on empty project
make dev                                    # PG/Redis/MinIO healthy
make dev-api &  curl -fsS localhost:8080/healthz
cd dashboard && npm run dev &               # placeholder renders on :5173
```

## M1 — Contracts: OpenAPI + schema

**Tasks**: author `api/openapi/openapi.yaml` covering the **entire** surface of [`05-api.md`](05-api.md) (yes, all of it — endpoints not yet implemented return 501 from generated stubs); oapi-codegen + sqlc + openapi-typescript wired into `make generate`; migration `0001_init.sql` per [`04-database.md`](04-database.md); goose embedded + boot migration; config package (full env reference from [`10-deployment.md`](10-deployment.md)); `httpx` problem+json rendering; request-ID + logging + recovery middleware; `/readyz`, `/metrics`.

**Acceptance**:
```
make generate && git diff --exit-code       # clean gen
vacuum lint api/openapi/openapi.yaml        # zero warnings
make dev-api && curl -s localhost:8080/api/v0/event | jq .status  # 501 problem+json
goose -dir api/internal/db/migrations postgres "$DSN" up && goose ... down-to 0 && up
```

## M2 — Auth & sessions (F1)

**Tasks**: argon2id hashing; Redis sessions + `sess:user:` sets; register/login/logout/me/password endpoints; session + origin-check + rate-limit middleware; login timing uniformity; seed admin on boot; audit log writes for admin-relevant actions (infra now, used from M4).

**Acceptance**: unit tests for PHC round-trip & dummy-hash login path; integration test register→login→me→logout→401; `curl` happy path documented in AGENTS.md works; rate limit returns 429 with `Retry-After` after 10 rapid logins.

## M3 — Teams (F2)

**Tasks**: teams service (create/join/leave/rename/invite regen, captain transfer, max size, one-team-per-user), public team/user profile endpoints (solves array empty for now), admin user/team list + PATCH (ban/hide/role) + password reset.

**Acceptance**: integration tests: full team lifecycle incl. captain-leaves transfer; ban revokes sessions (login → ban via admin → next request 401); `GET /teams` excludes hidden.

## M4 — Event & challenges admin (F3, F4, F5)

**Tasks**: event GET/PATCH with window validation + default-event seeding; challenges service + admin CRUD (separate admin/participant schemas!); attachment upload/stream via `ObjectStore` (S3Store + bucket bootstrap); participant challenge list/detail with visibility + phase gating; markdown left raw (frontend renders).

**Acceptance**: integration: create challenge invisible → participant 404 → toggle visible → participant sees it without flag field in JSON (`jq 'has("flag")' == false`); upload 1 MB file → download bytes identical (`sha256sum`); pre-start `GET /challenges` → 403.

## M5 — Submissions & scoring (F6, F7) + scoreboard REST (F8 part)

**Tasks**: `scoring` package with both engines (test vectors from [`07-scoring.md`](07-scoring.md)); submissions service (tx flow from [`01-architecture.md`](01-architecture.md), constant-time compare, rate limits, max_attempts); `scoreboard.Compute` + Redis cache + all recompute triggers; freeze ticker + frozen snapshot; `GET /scoreboard`; solves arrays on team/user profiles now real.

**Acceptance**: scoring unit vectors pass; the concurrent-double-solve race test passes with `-race`; integration: two teams solve at different times → standings order & dynamic values match a hand-computed fixture (write the fixture in the test as a table); freeze integration test (clock injected) shows frozen REST reads.

## M6 — Live scoreboard (F8)

**Tasks**: `ws` hub (register/unregister, broadcast with 1 s throttle latest-wins, ping/pong, drain on shutdown); `/api/v0/ws` endpoint; `event.phase` ticker transitions; wire recomputes → broadcast.

**Acceptance**: integration test with two `coder/websocket` clients: connect → `hello` + `scoreboard`; a solve → both receive updated standings ≤ 2 s; kill one client → hub gauge decrements (check `/metrics`).

## M7 — Container runtime (F9)

**Tasks**: `runtime` types + `DockerRuntime` per [`08-challenge-runtime.md`](08-challenge-runtime.md) (create settings table exactly as specced); port allocator; instance admin endpoints (deploy/status/restart/destroy/logs); reconcile on boot + ticker; connection info rendering in participant challenge payloads; `FakeRuntime` for non-Docker tests.

**Acceptance**: build-tagged integration (`dockerint`): deploy nginx-based fixture challenge → state `running`, TCP dial on allocated port succeeds, logs endpoint returns output, destroy frees the port for the next allocation; orphan-container test (create labeled container manually → reconcile removes it); daemon-down test via bogus `OSCTF_DOCKER_HOST` → deploy returns runtime-unavailable problem, `readyz` still 200.

## M8 — Frontend: participant (F11 + F6/F8 UI)

**Tasks**: everything participant-facing in [`09-frontend.md`](09-frontend.md): auth pages, landing + countdown, challenge board/detail with submit UX, scoreboard with WS client + poll fallback + frozen banner, team pages, profile; theme system; ApiError plumbing; testids.

**Acceptance**: `npm test` component suites green; Playwright flow 1 (participant golden path) green against composed stack; manual: kill API mid-session on scoreboard → UI degrades to polling message, recovers on restart.

## M9 — Frontend: admin (F10 UI)

**Tasks**: admin dashboard, event settings, challenge list/editor (incl. instance panel with logs viewer), users/teams tables, submission log with filters + auto-refresh.

**Acceptance**: Playwright flows 2 (admin lifecycle) and 3 (freeze) green; manual: full container challenge lifecycle (create → deploy → participant connects → logs → destroy) through the UI only.

## M10 — Examples & seeding (F12, F13)

**Tasks**: `challenge.yaml` parser; the 8 example challenges per [`13-example-challenges.md`](13-example-challenges.md) with `make examples` building the 4 container images; seeder (admin, default event, examples); first-boot orchestration per [`10-deployment.md`](10-deployment.md); `scripts/smoke.sh`.

**Acceptance**:
```
docker compose down -v && make examples && docker compose up -d --build --wait
scripts/smoke.sh                            # all 12 assertions pass
```
Plus: solve every example challenge manually per its intended solution (checklist in 13) — an example that isn't solvable is a release blocker.

## M11 — Hardening, docs, release (F14, F15)

**Tasks**: `docs/guides/` — `install.md`, `authoring.md` (challenge.yaml reference + shared-instance caveats + isolation warnings), `running-an-event.md` (timeline: install → configure → test → run → freeze → backup); README rewrite (humans); final `AGENTS.md` pass (gotchas section from real friction encountered); security headers verification; load sanity check (`hey -z 30s -c 100` on `/scoreboard` and `GET /challenges` — p99 < 250 ms on the reference box, record numbers in the PR); dependency audit (`govulncheck`, `npm audit`); tag `v0.1.0`, publish image, CHANGELOG.

**Acceptance**: smoke + full CI green on the tag; success criteria 1–5 in [`00-overview.md`](00-overview.md) each explicitly checked off in the release PR description; a fresh machine (or clean VM) walkthrough of `install.md` succeeds start-to-finish.

---

## Definition of done (every milestone)

- CI green, including generate-drift.
- New behavior covered at the layer where it can break (see [`11-testing-ci.md`](11-testing-ci.md)).
- `AGENTS.md` still accurate (setup, tasks, gotchas).
- No scope pulled in from the OUT list in [`00-overview.md`](00-overview.md).
