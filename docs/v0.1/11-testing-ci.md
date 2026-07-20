# 11 — Testing & CI

## Test pyramid

| Layer | Tooling | Scope | Speed budget |
|---|---|---|---|
| Unit (Go) | stdlib `testing`, `-short` | scoring math (exact vectors from 07), auth crypto, port allocation, config parsing, services with store interfaces faked where cheap | < 30 s total |
| Integration (Go) | testcontainers-go (PG, Redis, MinIO) | store layer against real Postgres; service flows (register→team→submit→scoreboard) through real DB+Redis; runtime tests against the host Docker daemon (build-tagged `dockerint`, skipped when no daemon) | < 5 min |
| Unit (TS) | Vitest + Testing Library | components with logic: flag submit states, countdown, ApiError mapping, WS cache updates (mocked socket) | < 60 s |
| E2E | Playwright against `docker compose up` | the 3 golden flows below | < 5 min |
| Smoke | `scripts/smoke.sh` (bash + curl + jq) | API-level golden path against the composed stack | < 2 min |

Coverage gate: **80% on `scoring`, `auth`, `submissions`, `scoreboard`** (the money paths); no repo-wide percentage gate (they rot into theater). Enforce via `go test -coverprofile` + a small script checking those packages.

### Test conventions

- Table-driven tests; `t.Parallel()` wherever safe; integration tests create isolated schemas/databases per test (testcontainers per package, schema per test — fast).
- No mock framework — hand-written fakes implementing the interfaces (they live next to the interface: `runtime/fake.go` exports `FakeRuntime` for handler/service tests).
- Time: services take the injectable clock (03) — window/freeze tests never sleep.
- The submission race test is mandatory: two concurrent correct submissions for one team+challenge → exactly one solve row, one 200-correct, one 409.

## The three Playwright flows (exact)

1. **Participant golden path**: register → create team → open challenges → open a standard challenge → submit wrong flag (see error state) → submit correct flag (see success + points) → scoreboard shows the team with points.
2. **Admin challenge lifecycle**: login as seeded admin → create standard challenge (visible) → it appears on the participant board (second browser context) → edit points → delete.
3. **Freeze behavior**: admin sets `freeze_at` in the past → participant scoreboard shows frozen banner and stops moving while a new solve lands (admin submissions via API helper).

Playwright runs against a compose stack seeded with examples; test users created via the API in `beforeAll`. Selectors: the `data-testid` contract in [`09-frontend.md`](09-frontend.md).

## scripts/smoke.sh (assertion list)

Runs with `BASE_URL` env (default `http://localhost:8080`), exits non-zero on first failure, prints each step:

1. `/healthz` 200; `/readyz` 200.
2. Register user A (random suffix) → 201 + cookie jar.
3. `GET /auth/me` → username matches.
4. Create team → invite code present.
5. Register user B, join with invite code.
6. Admin login (env creds) → `PATCH /admin/event` set window to now±1 h.
7. `GET /challenges` as A → seeded examples present.
8. Submit wrong flag to `sanity-check` → `correct:false`; submit `OSCTF{welcome_to_the_game}` → `correct:true`.
9. `GET /scoreboard` → team present with points > 0.
10. Duplicate correct submit → 409.
11. 11 rapid submissions to one challenge → at least one 429.
12. `GET /metrics` contains `osctf_submissions_total`.

## CI (GitHub Actions, `.github/workflows/ci.yml`)

Jobs (all trigger on PR + push to main; `[smoke]` also nightly on main):

| Job | Steps |
|---|---|
| `generate-drift` | `make setup-tools && make generate && git diff --exit-code` — generated code must be committed |
| `api-lint` | golangci-lint (pinned) + `vacuum lint api/openapi/openapi.yaml` (zero warnings) |
| `api-test` | `go test ./... -short -race` |
| `api-integration` | `go test ./... -race -run Integration` (testcontainers; Docker available on GH runners) + migration up-down-up check (`goose up && goose down-to 0 && goose up` against a fresh PG) |
| `web` | `npm ci && npm run lint && npm run typecheck && npm test && npm run build` |
| `image` | `docker build .` (no push on PR; push `ghcr.io` on main tag) |
| `smoke` | needs `image`: `docker compose up -d --build --wait` → `scripts/smoke.sh` → `docker compose logs platform` on failure → down -v |
| `e2e` | needs `image`: compose up → `npx playwright test` (chromium only) |

Branch protection on `main`: all jobs required. Keep total PR pipeline under ~12 minutes; if it creeps past, split `e2e` to main-only — do not delete tests to go faster.

## What is *not* tested in v0.1 (explicit)

- Load/perf testing (success criterion 4 in `00` is validated manually with `hey` against `/scoreboard` and documented, not CI-gated).
- Browser matrix (chromium only), mobile layouts (spot-check manually).
- Chaos (kill -9 postgres) — boot-retry behavior is covered by the compose `depends_on: service_healthy` + a single integration test for connect-retry.
