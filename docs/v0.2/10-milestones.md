# 10 — Milestones (Build Plan)

Execute in order on top of the shipped `v0.1.0` code. Each milestone lists **tasks**,
**deliverables**, and **acceptance** — commands/checks that must succeed (run from the repo
root) before moving on. The overriding invariant across every milestone: **v0.1
`shared`/`static` behaviour is preserved** — the v0.1 test suites stay green throughout.

Dependencies are linear except where noted. Start each milestone by confirming the previous
one's acceptance still passes.

---

## M0 — Migration & contracts (schema + OpenAPI first)

**Tasks**: write `0002_dynamic_instances.sql` (up + down) per [`02-database.md`](02-database.md)
— new `challenges` columns, `instances.team_id/flag/expires_at/network`, partial unique
indexes, widened host-port CHECK. Extend `openapi.yaml` with the new participant instance
endpoints, admin instances endpoints, changed `ChallengeDetail`/`AdminChallengeInput`, and
the `TeamInstance`/`AdminInstance` schemas ([`06-api.md`](06-api.md)). Add the sqlc queries
([`02-database.md`](02-database.md)). Extend `config` with the new env + validation
([`08-deployment.md`](08-deployment.md)). Regenerate `apigen`, `db/gen`, TS client. New
endpoints return 501 from generated stubs until later milestones.

**Acceptance**:
```
make generate && git diff --exit-code        # clean gen (drift gate)
goose -dir api/internal/db/migrations postgres "$DSN" up && \
  goose ... down-to 0 && goose ... up         # 0002 up/down/up clean
cd api && go test ./internal/config/...        # new env parsing/validation
cd api && go build ./...                        # compiles with new gen
```
Backwards check: a v0.1 DB migrates up; every existing challenge reads `instancing=shared`,
`flag_mode=static`, `egress=true`.

## M1 — Runtime: per-team deploy + hardening

**Tasks**: extend `InstanceSpec` (owner/network/internal/rootfs/tmpfs); Manager
`DeployForTeam`/`DestroyInstance(id)`/`GetTeamInstance`/`ListTeamInstances`/`CountTeamRunning`
+ rename shared path ([`03-runtime.md`](03-runtime.md)); `DockerRuntime` per-team networks
(`ensureNamedNetwork`, `Internal`), read-only rootfs + tmpfs + `writable_paths`, attach to
`spec.NetworkName`; network GC in `Reconcile`; `FakeRuntime` spec parity + `Deployed`
capture.

**Acceptance**:
```
cd api && go test ./internal/runtime/...                       # unit + fake
cd api && go test -tags dockerint -race ./internal/runtime/... # real daemon
```
Dockerint asserts: read-only rootfs, `/tmp` tmpfs, per-team bridge, `--internal` on
`egress:false`, **team-A-cannot-reach-team-B** isolation probe, empty-network GC. Shared
deploy path unchanged (v0.1 runtime tests still pass).

## M2 — Flags: generation, injection, validation, sharing signal

**Tasks**: `flags.Generator` (crypto/rand, `osctf{…}`, prefix from env)
([`05-flags.md`](05-flags.md)); store `instances.flag` in `DeployForTeam` before deploy;
Manager injects `Env[FLAG]`=instance flag (else `challenges.flag`); submissions hot path
becomes flag-mode-aware inside the locked tx — per-instance compare, `403 no-instance`,
`flag.shared` audit + metric via `FindInstanceByFlag`; new `audit` action + `metrics`
counter.

**Acceptance**:
```
cd api && go test ./internal/flags/... ./internal/submissions/...
```
Tests: static path unchanged (regression); right-team flag solves; other-team flag →
incorrect + sharing signal; no-instance → 403; secret-leak scan finds no flag in any
serialized payload.

## M3 — Scheduler

**Tasks**: `scheduler` package ([`04-scheduler.md`](04-scheduler.md)) — `Start` (quota,
idempotent, flag+ttl, deploy), `Stop`, `Extend`; `RunExpiry` (30 s) and `CleanupEnded`
(folded into the phase ticker); injected `clock`; mutex + DB-invariant serialization;
metrics. Wire into `serve` (`main.go`): construct the scheduler, start `RunExpiry`, pass it
into `runTickers` for the `running→ended` edge.

**Acceptance**:
```
cd api && go test ./internal/scheduler/...    # quota, idempotency, extend cap, ttlFor
cd api && go test ./internal/scheduler/... -run Expiry   # fake-clock expiry pass
```
Expiry/cleanup are deterministic (injected clock, single pass called directly — no sleeps).

## M4 — Handlers & API wiring

**Tasks**: implement the strict-server handlers ([`06-api.md`](06-api.md)) —
`startInstance`/`stopInstance`/`extendInstance` → scheduler; `adminListInstances`/
`adminDestroyInstanceById`; `ChallengeDetail.instance` (caller-team) + `instancing`/`flag_mode`;
`adminCreate/UpdateChallenge` accept + validate the new authoring fields (`422` for
non-container); `adminListSubmissions` sharing badge/filter; the WS `instance` nudge (or the
documented poll fallback). Map scheduler errors to the right status codes.

**Acceptance**:
```
cd api && go test ./internal/handlers/... -run Integration
```
Covers: start `201`→idempotent `200`→stop; extend bumps `expires_at`; quota `409`;
`event-not-running` `409`; start on shared → `409 not-per-team`; two-team per-instance flag
flow with sharing audit row; `adminListInstances` shows both owner kinds, never a flag.

## M5 — Frontend

**Tasks**: participant instance panel in `ChallengeDialog` (Start/Stop/Extend, countdown,
states) + hooks; admin **Instances** page + nav entry + destroy; challenge-editor authoring
fields gated on `kind==='container'`; all new testids ([`07-frontend.md`](07-frontend.md)).

**Acceptance**:
```
cd dashboard && npm run lint && npm run typecheck && npm test && npm run build
```
Web unit tests: `useCountdown`, panel states, quota-error inline message. Editor round-trips
the new fields.

## M6 — Examples, seed, smoke

**Tasks**: `challenge.yaml` schema additions in the seed parser ([`11-example-challenges.md`](11-example-challenges.md));
build ≥ 2 per-team examples (one web `per_instance`, one pwn `per_instance`) + a hardening
demo; the examples serve their `FLAG`; `scripts/build-examples.sh` builds the new images;
extend `scripts/smoke.sh` with the instance leg ([`09-testing-ci.md`](09-testing-ci.md)).

**Acceptance**:
```
bash scripts/build-examples.sh                 # new images build
docker compose up -d --build --wait
set -a; source .env; set +a; BASE_URL=http://localhost:8080 bash scripts/smoke.sh
```
Smoke's instance leg: start a seeded `per_team` challenge → `201` + `host_port` → stop → row
gone. Seed is idempotent; a re-seed doesn't duplicate.

## M7 — E2E, CI, docs, release

**Tasks**: `dashboard/e2e/instance.spec.ts` golden flow ([`09-testing-ci.md`](09-testing-ci.md));
confirm all CI jobs green (drift, lint, test, integration incl. dockerint, web, image, smoke,
e2e); update `CHANGELOG.md` (v0.2.0), `.env.example`, `AGENTS.md`/README with the new
capability and env; upgrade note (widened firewall range, migration is additive). Tag
`v0.2.0`.

**Acceptance**:
```
# full local gate
cd api && go test ./... -race && go test -tags dockerint ./internal/runtime/...
cd dashboard && npm run lint && npm run typecheck && npm test && npm run build
docker compose up -d --build --wait && npx --prefix dashboard playwright test
```
The v0.1 golden flows (`participant`, `admin`, `freeze`) pass **unchanged**; the new
`instance` flow passes. All success criteria in [`00-overview.md`](00-overview.md#L80) are
demonstrably met. Then tag + release.

---

## Milestone → success-criterion map

| Criterion ([00](00-overview.md#L80)) | Proven in |
|---|---|
| 1 — two teams, distinct flags, correct crediting | M2 (unit) + M4 (integration) + M7 (e2e) |
| 2 — TTL expiry + event-end cleanup, no operator | M3 (fake-clock) + M4 |
| 3 — quota `409`, stop frees slot | M3 + M4 |
| 4 — per-team network isolation | M1 (dockerint) |
| 5 — read-only rootfs + tmpfs don't break examples | M1 + M6 |
| 6 — v0.1 event unchanged | every milestone's backwards check + M7 |
| 7 — sharing signal in admin log | M2 + M4 |

## Notes for the building agent

- **Migration and OpenAPI first (M0).** Everything downstream generates from them; get the
  drift gate green before writing logic.
- **Preserve, don't rewrite.** When you touch `runtime`/`submissions`/`handlers`, keep the
  v0.1 code paths intact and branch on `instancing`/`flag_mode`.
- **Secrets discipline is a build rule, not a polish step.** Never let a flag reach a log,
  audit meta, metric label, or response — enforced by the M2 leak-scan test.
- **When a detail is unspecified**, apply the Core Principles, pick the boring option, and
  record it in that doc's `## Decision log`. No TODOs in code.
