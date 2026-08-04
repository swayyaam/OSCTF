# AGENTS.md — OSCTF

> First-class onboarding for coding agents and humans. Update this file in the same
> change as anything that invalidates it (setup, tasks, gotchas).

## What this is

OSCTF is a self-hostable platform for cybersecurity competitions and training. v0.1 (the
current target) lets one person host a real CTF for ~100 participants on a single server.
The authoritative build spec is [`docs/v0.1/`](docs/v0.1/README.md); the vision is
[`docs/project-desc.md`](docs/project-desc.md).

## Setup (clean clone → running dev environment)

Prereqs: Go 1.25+, Node 22+, Docker, Make.

```bash
make setup            # install pinned Go tools (oapi-codegen, sqlc, goose, vacuum,
                      #   golangci-lint) + `npm ci` in dashboard/
cp .env.example .env  # edit OSCTF_ADMIN_EMAIL / OSCTF_ADMIN_PASSWORD
make dev              # start Postgres, Redis, MinIO (compose)
make dev-api          # run the API on :8080  (source ../.env is automatic)
make dev-web          # in another shell: Vite dev server on :5173
```

Expected URLs:

- API: <http://localhost:8080> (`curl http://localhost:8080/healthz` → `ok`)
- Web (dev): <http://localhost:5173> (proxies `/api` → :8080)
- MinIO console (dev): <http://localhost:9001>

> `make setup` builds sqlc from source, which fails on some macOS SDKs (a cgo
> `strchrnul` conflict in its Postgres parser). If it does, install the prebuilt binary:
> `curl -fsSL https://downloads.sqlc.dev/sqlc_1.28.0_darwin_arm64.tar.gz | tar xz -C "$(go env GOPATH)/bin"`.

## Architecture map

Modular monolith: one Go process (`platform serve`) with enforced package boundaries.
Details in [`docs/v0.1/01-architecture.md`](docs/v0.1/01-architecture.md).

| Directory | What |
|---|---|
| `api/cmd/platform` | Entrypoint; subcommands `serve|migrate|seed`. Composition root (`main.go`). |
| `api/openapi` | `openapi.yaml` — the hand-authored API contract. Codegen input. |
| `api/internal/config` | Env → typed `Config`. stdlib + env parser only. |
| `api/internal/db` | pgx pool, embedded goose migrations, `WithTx`. |
| `api/internal/apigen` | Generated oapi-codegen server interface + types (checked in). |
| `api/internal/handlers` | Implements the generated interface; HTTP ↔ service. No business logic. |
| `api/internal/httpserver` | chi router, middleware, `/healthz` `/readyz` `/metrics`, SPA fallback. |
| `api/internal/httpx` `apperr` | problem+json rendering; the domain error vocabulary. |
| `api/internal/auth users teams events challenges submissions` | Domain services. |
| `api/internal/scoring` | Pure scoring engines (no I/O, no clock). |
| `api/internal/scoreboard` `ws` | Standings computation + cache; WebSocket hub. |
| `api/internal/runtime` `storage` | `ChallengeRuntime` (Docker) and `ObjectStore` (MinIO). |
| `api/internal/scheduler` `flags` | Per-team instance lifecycle (spawn/expire/quota) and per-instance flag generation (v0.2; see [`docs/v0.2/`](docs/v0.2/README.md)). |
| `api/internal/webdist` | Embeds the built SPA (build tag `embed_spa`; dev fallback otherwise). |
| `dashboard/` | React 19 + TS SPA (Vite). |
| `examples/challenges/` | Seeded example challenges (`challenge.yaml`). |
| `deploy/` | Prometheus/Grafana/Caddy configs (compose profiles). |

## Common tasks

- **Add an endpoint:** edit `api/openapi/openapi.yaml` → `make generate` → implement the
  new method on the handler in `api/internal/handlers` → call a service → add an sqlc query
  in `api/internal/db/queries/*.sql` (then `make generate` again) → test.
- **Add a migration:** `make migrate-new name=<slug>`, write `-- +goose Up`/`Down`,
  then `make generate` (sqlc reads the schema) and restart the API (migrates on boot).
- **Add a frontend route:** add a page under `dashboard/src/pages`, register it in the
  router; fetch data through a hook in `dashboard/src/api/hooks`.
- **Run one Go test:** `cd api && go test ./internal/scoring -run TestDynamic -v`.
- **Debug a failing container instance:** admin UI instance panel (logs), or
  `docker ps --filter label=osctf.managed=true` and `docker logs <id>`.

## Conventions (abridged; full text in docs/v0.1/01 and /03)

- `handlers` never touch `db` directly — always via a service. Services never import HTTP.
- Only `main.go` and an impl's own tests may import a concrete core-interface implementation.
- `scoring` is pure: numbers in, numbers out. No I/O, no `time.Now()`.
- Every cross-package call takes `context.Context` first. Wrap errors with context.
- Never log flags, passwords, session tokens, or password hashes.
- Generated code (`apigen/`, `db/gen/`, `dashboard/src/api/schema.d.ts`) is committed;
  CI fails on drift. Regenerate with `make generate`.
- Conventional Commits; no `TODO`/`FIXME` comments (lint enforces).

## Testing contract

A handful of invariants carry the platform's security and correctness; each is pinned by a
named test. **These tests are never weakened, `t.Skip`'d, build-tagged off, or deleted to
make a build go green.** If one fails, either the code is wrong (fix the code) or the
invariant changed on purpose — in which case update the test and say why, here or in the
linked doc, in the same change.

| Invariant | Pinned by |
|---|---|
| Every route has an authorization-policy entry; the matrix holds across identity × phase | `handlers.TestPolicyTableCoversEveryRoute`, `TestPolicyMatrixIntegration`, `TestPolicyMatrixWebSocketIntegration` |
| Freeze fails closed — a failed events read never serves a live board | `handlers.TestFreezeFailsClosedWithoutEvents` + `scoreboard_freeze_integration_test.go` |
| A hidden/unreleased resource is indistinguishable from a nonexistent one (status, body, ~timing) | `handlers/enumeration_integration_test.go` |
| No flag reaches any participant surface (REST/WS/logs/metrics/audit) | `handlers/flag_containment_integration_test.go` |
| The Docker adopt/GC label keys are exactly as specified | `runtime.TestLabelContract` |
| Reconcile's decisions are correct and conservative (pure table) | `runtime.TestReconcileDecisions` (+ action-order, future-row) |
| WS frames arrive in order — hello before any board, phase never reordered past its snapshot | `handlers.TestWSFrameOrderingIntegration`, `ws/hub_test.go` |
| Lists serialize as `[]`/`{}` never `null` at every level; response shapes pinned; WS data ≡ REST data | `handlers.TestGoldenEmpty`/`TestGoldenFull`, `TestSerializationZeroRowListsIntegration`, `ws.TestWSFrameGolden`/`TestWSScoreboardMatchesREST` |
| The WS frame-type set is identical on both sides of the wire | `ws.TestFrameTypesContract` + `dashboard/src/ws/frame-types.contract.test.ts` |
| argon2id hashing is concurrency-bounded (no OOM) and sheds with 503 | `auth.TestHashGate*` |
| Package boundaries hold (no service↔service except foundational auth/events; Docker/MinIO/Redis confined) | `depguard` in `.golangci.yml` |
| No goroutine outlives the tests; no Docker container/bridge/volume residue survives reconcile | `goleak.VerifyTestMain` (ws, scheduler, handlers, runtime) + the dockerint `assertNoResidue` guard |

Rules that keep the surface from eroding:

- **A bug fix ships with a failing-test-first reproduction** — add the test, watch it fail, then fix.
- **A new route requires a policy-table entry**, or `TestPolicyTableCoversEveryRoute` fails CI.
- **A new participant-facing endpoint is added to the flag-containment scanner's probe list.**
- **A new WS frame** goes in `internal/ws/frames.go` (+ regenerate `frame_types.json` with
  `UPDATE_GOLDEN=1`) **and** the dashboard's `KNOWN_FRAME_TYPES`, or both contract tests fail.
- **A new list-returning endpoint** gets empty + full serialization goldens.
- **A new domain service does not import another** (depguard) — depend on foundational `auth`/`events`, or invert the dependency. `auth` and `events` are foundational *because* they are identity and event-phase authority; that list does not grow by analogy.
- **All Go CI jobs run `-shuffle=on`** — a failure there is inter-test state leakage, not a flake; fix the leakage, don't re-order or `-count=1` around it.
- Regenerate goldens deliberately (`UPDATE_GOLDEN=1`) and eyeball the diff — never to silence a failure you don't understand.

A meaningful negative result worth keeping: after the v0.2 → v0.2.1 rewrite of the
concurrency surface — per-team scheduler locks, the ordered WS client queue, the WS
admission gate, the stale-row reaper, and the graceful-shutdown background join — both
`-shuffle=on` (all tiers) and `goleak` (ws/scheduler/handlers/runtime) came back **clean**.
That is evidence the surface is sound, not an absence of testing. Keep both instruments in
place so the next change to that surface is held to the same bar.

Known coverage gaps (accepted and tracked — don't mistake green for covered):

- `FakeRuntime` models containers but **not networks** (see Gotchas): network decisions are
  covered by the pure Reconcile table + `dockerint`, not through the fake.
- **Reconcile's grace timing does not track the injected clock — by design, so the soak
  cannot reach it by accelerating time.** Grace is `clock_timestamp() - updated_at >=
  reconcileGrace`, and `updated_at` is written by Postgres `now()` (DB wall clock, from many
  paths incl. SQL defaults), so both operands are DB-wall time — the fix from 2b-2 that
  stopped app-vs-DB skew from making every row read "fresh." The consequence: the soak's
  120× injected clock cannot compress the ~150 s grace (real DB seconds don't accelerate), so
  the vanish→lost→reap path is unreachable within a 2 m run unless a row's `updated_at` is
  aged via SQL (`soak -age-lost-rows`, on by default; the same technique as the `2d`
  reconcile integration tests). Measured: aging on `reaped=16` vs off `reaped=10` at seed 1 —
  the ~6 delta *is* that path. The grace *arithmetic* is covered by the pure `Reconcile`
  table tests (future-dated row, boundary) and by `dockerint`; the soak covers everything
  *around* the mark-lost (reconcile executing it, the reaper reclaiming the port, invariants
  holding) but not the wall-time passage itself. Do not "fix" this by wiring the fake's grace
  to the injected clock — that reintroduces the 2b-2 skew and diverges the fake from the real
  runtime, which reads the identical `ReconcileClock` query.
- **Docker Desktop does not enforce the isolation `dockerint` probes** — the probe reports
  "not enforced" and `VerifyIsolation=false` there (GitHub issue #2, targeted v0.3/v0.4);
  real enforcement is validated only against a Linux daemon.
- The scheduler **executor's partial-failure policy is continue-on-error** — a failed step
  does not roll back earlier ones; there is no all-or-nothing test because that is not the contract.
- Four list endpoints (`admin/instances`, `teams/mine`, `teams/{id}`, `challenges/{slug}`)
  are outside the integration zero-row layer; their shape and `[]`-not-null are pinned by the
  unit serialization goldens, not end-to-end.
- The integration tier needs `DOCKER_HOST` pointed at the daemon on Docker Desktop
  (`unix://$HOME/.docker/run/docker.sock`) — testcontainers can't autodetect it there.
- **`cmd/platform` is deliberately NOT goleak-wired.** `TestWaitBoundedTimesOutOnWedgedWorker`
  leaves a goroutine wedged on purpose to exercise the shutdown-timeout path, so
  `goleak.VerifyTestMain` there would always fail. Do not "fix" the missing TestMain — the
  fix is a false one that ends in weakening that test to make it pass. The real background
  join (`bgWG` + `waitBounded`) is validated by that test's design and by the goleak-clean
  `handlers`/`scheduler` integration flows that drive the same workers.

## Gotchas

- **Regenerate after editing `openapi.yaml` or the SQL schema**, or the build breaks
  (`make generate`). The generated code is the contract handlers compile against. The
  OpenAPI is authored as 3.0.3 (oapi-codegen can't parse 3.1); lint it with the pinned
  ruleset (`vacuum lint -r api/openapi/vacuum-ruleset.yaml -d api/openapi/openapi.yaml`).
- The dev build serves an SPA placeholder — that's expected. The real SPA is embedded
  only in `-tags embed_spa` builds (the Docker image); in dev use `make dev-web`.
- `make dev-api` requires the dev datastores (`make dev`) first, and overrides the
  datastore URLs to localhost with Postgres on **55432** (not 5432, to dodge a
  natively-installed Postgres). It also sets `OSCTF_CORS_DEV_ORIGIN=http://localhost:5173`
  — without it, browser mutations from the Vite dev origin get a 403 origin-check failure.
- Integration tests need Docker (testcontainers) and skip under `-short`. The
  container-runtime tests are build-tagged: `go test -tags dockerint ./internal/runtime/...`.
  On Docker Desktop, testcontainers can't autodetect the socket — export
  `DOCKER_HOST=unix://$HOME/.docker/run/docker.sock` first.
- **Known coverage gap — `FakeRuntime` models containers but NOT networks.** Reconcile's
  network decisions (team-network GC, `team_id` protection, the v0.2 missing-label
  flag) are therefore covered by the pure `Reconcile` table tests
  (`internal/runtime/reconcile_test.go`) plus the real-daemon `dockerint` tests, but not
  through the fake the way container decisions are. This is deliberate: per-team bridges
  are a Docker-only concept, so a fake network model would exercise trivial fake code,
  not the real `NetworkRemove`/inspect path — that path is what `dockerint` covers. To
  strengthen network coverage, add `dockerint` tests (real bridges), not fake modelling.
- **Playwright e2e runs with `workers: 1`** — the flows mutate one shared global event
  (window/freeze) and must not run concurrently.
- Registration is rate-limited per IP (`OSCTF_REGISTER_IP_BURST`, default **500** per
  `OSCTF_REGISTER_IP_WINDOW`=600s — raised from the original 5/hour, GitHub issue #1). A
  deliberate flood or a stale counter can still trip it; `docker compose exec redis
  redis-cli FLUSHALL` clears the counters locally.
- If `make setup` fails building sqlc (a macOS cgo `strchrnul` conflict), install the
  prebuilt binary — see Setup above.
- The platform mounts the Docker socket in production — **root-equivalent on the host**.
  Run events on a dedicated VM. Example container images are built on the host by
  `make examples` and found via the if-not-present pull policy at deploy time.
