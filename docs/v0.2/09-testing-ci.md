# 09 — Testing & CI

Same testing philosophy and the same CI jobs as v0.1
([`../v0.1/11-testing-ci.md`](../v0.1/11-testing-ci.md)); v0.2 **adds tests**, it does not
restructure the pipeline. The existing v0.1 suites must keep passing unchanged — that is the
backwards-compatibility proof.

The v0.1 CI jobs are: `generate-drift`, `api-lint`, `api-test`, `api-integration`, `web`,
`image`, `smoke`, `e2e`. v0.2 slots new tests into `api-test`, `api-integration`, `web`, and
`e2e`; the migration up/down/up job already exercises `0002` for free.

## Unit tests (`api-test`, no containers)

| Package | New tests |
|---|---|
| `scheduler` | Quota enforcement (Start at limit → `409`; Stop frees a slot). Idempotent Start (two calls → one instance). Extend cap math (`min(now+extend, started+maxTTL)`; past-max → `409`). `ttlFor` (per-challenge override, `0` → nil). All against `FakeRuntime` + an **injected clock**. |
| `scheduler` (expiry) | With a fake clock: instance `expires_at=t0+1h`, advance clock, run **one expiry pass** directly → `DestroyInstance` called, row gone. Deterministic, no sleeps. |
| `scheduler` (cleanup) | On `running→ended` edge, all per-team rows destroyed, shared rows untouched. |
| `flags` | Generator: format `osctf{…}`, ≥128-bit entropy, uniqueness across N calls; prefix honored. |
| `submissions` | Per-instance compare: right team's flag → correct; another team's flag → incorrect **and** sharing signal raised; no instance → `403 no-instance`; static path unchanged (regression). |
| `config` | New env parsing + validation (TTL/extend/maxTTL ordering, quota ≥ 1, widened port range). |
| `runtime` (manager) | `DeployForTeam` sets spec `TeamID`, `NetworkName`, `Internal=!egress`, `ReadonlyRootfs`, tmpfs incl. `writable_paths`, `Env[FLAG]`=instance flag — asserted via `FakeRuntime.Deployed` capture. |

Assert the **secret invariants** in tests too: a test that scans serialized participant/admin
payloads for the flag string and fails if present (belt-and-braces for [`05-flags.md`](05-flags.md) rule 1).

## Integration tests (`api-integration`, testcontainers + real Postgres)

Named `*Integration` (run by `go test ./... -run Integration`). New/extended:

- `handlers/instances_integration_test.go` (extend): participant `startInstance` →
  `201` + `TeamInstance`; second Start → `200` idempotent; `stopInstance` frees the row;
  `extendInstance` bumps `expires_at`; quota `409`; `event-not-running` `409`;
  `startInstance` on a `shared` challenge → `409 not-per-team`.
- Two-team per-instance flag flow (real DB): team A and team B each Start a `per_instance`
  challenge → distinct `instances.flag`; A submits A's flag → solve; A submits B's flag →
  incorrect + `flag.shared` audit row present; scoreboard credits only the real solves
  (ties into the v0.1 scoreboard integration test).
- `adminListInstances` returns both owner kinds with `team_name`/`network`; never a flag.
- Migration test asserts `0002` up **and** down (the CI `migration up-down-up` job covers
  up/down/up; add a Go test that per-team rows block the down until drained, matching the
  documented contract).

## Docker runtime tests (`api-integration`, `-tags dockerint`, real daemon)

Extend `runtime/docker_integration_test.go`:

- **Hardening:** deploy a container; inspect it; assert `ReadonlyRootfs=true`, `/tmp` tmpfs
  present, `no-new-privileges`, cap-drop ALL, pids/mem limits — the whole hardening set.
- **Per-team network:** two per-team instances of the same image on `osctf-team-A` and
  `osctf-team-B`; assert each container is on its own bridge; with `egress:false` the
  network is `--internal`.
- **Isolation (success criterion #4):** from team A's container, attempt to reach team B's
  container's internal port over the Docker network → connection fails. (Exec a short
  `nc`/`/dev/tcp` probe inside A's container against B's container IP.)
- **Network GC:** destroy a team's last instance, run `Reconcile`, assert the empty
  `osctf-team-*` network is removed; a network still in use is kept.

These run only where a real Docker daemon is available (the `dockerint` tag gates them, as
in v0.1).

## Web unit tests (`web`)

- `useCountdown` hook: formats `mm:ss`, warning class ≤ 5 min, "expired" at 0.
- Instance panel rendering: null → Start; running → connection info + countdown + Stop/Extend;
  error → Retry; quota `409` → inline message. (React Testing Library, mocked hooks.)

## E2E (`e2e`, Playwright, `workers:1`, `retries:0`)

Add one golden flow, `dashboard/e2e/instance.spec.ts`, alongside the v0.1 three
(participant, admin, freeze). Keep the v0.1 rate-limit-aware discipline (retries:0, reuse
logins) from the v0.1 e2e fixes.

**Per-team instance flow** (needs a seeded `per_team`/`per_instance` example challenge and
a real Docker daemon in the compose stack — the CI e2e job already runs compose):

1. Admin creates/【seed provides】a `per_team` + `per_instance` container challenge and makes
   it visible; event `running`.
2. Team A registers, opens the challenge dialog → **Start** (`instance-start`) → wait for
   `instance-state` = running and `connection_info` present.
3. Team A reads the flag from its instance (the example serves `FLAG`) and submits →
   scoreboard shows the solve.
4. **Extend** (`instance-extend`) → `instance-countdown` increases.
5. **Stop** (`instance-stop`) → panel returns to Start; admin **Instances** page
   (`admin-instances-table`) no longer lists it.
6. (Optional, if fast enough) a short-TTL challenge expires and the panel shows "expired".

Poll-for-propagation like the v0.1 freeze spec (poll `getChallenge`/the instances API for
the expected state before asserting the UI) rather than fixed sleeps, to stay
non-flaky.

## Smoke (`smoke`)

Extend `scripts/smoke.sh` with a per-team instance leg: authenticate, `POST
/challenges/{slug}/instance` on a seeded `per_team` challenge, assert `201` + a `host_port`,
`DELETE` it, assert the row is gone. Guard it so a stack without a working Docker daemon
degrades gracefully (the v0.1 smoke already tolerates runtime-optional paths).

## CI additions summary

| Job | Change |
|---|---|
| `generate-drift` | Unchanged — but you must commit regenerated `apigen` + TS client after editing `openapi.yaml`, or it fails. |
| `api-lint` | Unchanged (golangci-lint v2 via action@v7). New code must pass; no `//nolint` without a reason. |
| `api-test` | Runs the new scheduler/flags/submissions/config/manager unit tests automatically. |
| `api-integration` | Runs the new instance/flag/admin integration + extended dockerint runtime tests. |
| `web` | Runs the new hook/panel unit tests. |
| `image` | Unchanged. |
| `smoke` | Extended `smoke.sh` (instance leg). |
| `e2e` | New `instance.spec.ts` golden flow. |

## Backwards-compatibility gate (must stay green)

The v0.1 `participant.spec.ts`, `admin.spec.ts`, `freeze.spec.ts`, the v0.1 integration
suites, and the existing smoke run **unchanged**. If any needs editing to pass, that is a
signal v0.2 broke v0.1 behaviour — fix the code, not the test (success criterion #6).

## Decision log

- **Injected clock for all TTL/expiry tests.** Deterministic, no `time.Sleep`; mirrors the
  v0.1 freeze-snapshot test.
- **Isolation asserted with a real in-container probe.** The only honest way to prove
  network isolation; gated behind the `dockerint` tag like other real-daemon tests.
- **One new e2e golden flow, not many.** E2e is expensive and rate-limit-sensitive; unit +
  integration carry the detailed coverage, e2e proves the wired path once.
- **Secret-leak scan in tests.** Cheap insurance that no serializer ever grows a flag field.
