# OSCTF Testing / Regression Audit

Read-only audit of the codebase as it stands (HEAD `e341c7f`). Purpose: an accurate,
blunt picture of what exists, so a regression strategy can be built on facts, not intent.
Every claim cites `file:line`. Where the code's comment claims one thing and the behaviour
is weaker, the behaviour is what's reported.

Scope note: the backend (`api/`) is where the risk is. The dashboard is a thin typed
client of the API; it gets proportionally less depth here.

---

## 1. PACKAGE MAP

### Tree (`api/internal` + `api/cmd`), one line each

| Package | Purpose |
|---|---|
| `cmd/platform` | Server entrypoint; `serve|migrate|seed` switch; composition root (`main.go`). |
| `internal/apigen` | Generated oapi-codegen strict server + types (checked in). |
| `internal/apperr` | Domain error vocabulary; mapped to problem+json by httpx. |
| `internal/audit` | Writes `audit_log` rows; meta must exclude secrets. |
| `internal/auth` | argon2id passwords, Redis sessions, the `AuthProvider` interface. |
| `internal/challenges` | Challenge domain: admin CRUD, participant read, attachments, validation. |
| `internal/clock` | Injectable `type Clock func() time.Time`. |
| `internal/config` | Env → typed `Config` + validation. |
| `internal/db` | pgx pool + embedded goose runner. |
| `internal/db/gen` | sqlc-generated queries/models (checked in). |
| `internal/db/migrations` | Embedded SQL migrations `0001`, `0002`. |
| `internal/db/queries` | sqlc source `.sql`. |
| `internal/events` | Single-event window service (phase, freeze); NOT an event bus. |
| `internal/flags` | Per-instance dynamic flag generator (crypto/rand). |
| `internal/handlers` | Implements the generated interface; HTTP↔service mapping + per-handler authz. |
| `internal/httpserver` | chi router, middleware stack, `/healthz` `/readyz` `/metrics`, SPA fallback. |
| `internal/httpx` | problem+json rendering, request-ID, client-IP. |
| `internal/metrics` | Prometheus registry + custom metrics. |
| `internal/pagination` | page/per_page → limit/offset. |
| `internal/redisx` | Shared redis client + sliding-window rate limiter. |
| `internal/runtime` | `ChallengeRuntime` iface, `DockerRuntime`, `FakeRuntime`, `Manager` (rows/ports/spec). |
| `internal/scheduler` | Per-team instance lifecycle (start/stop/extend/expire/cleanup). |
| `internal/scoreboard` | Standings compute + Redis cache + freeze snapshot. |
| `internal/scoring` | Pure static/dynamic point engines. |
| `internal/seed` | Idempotent first-boot seeding (admin, event, example challenges). |
| `internal/storage` | `ObjectStore` iface + MinIO/S3 impl. |
| `internal/submissions` | Flag-submission tx: lock, attempts, constant-time compare, sharing signal. |
| `internal/teams` | Team domain: create/join/leave/rename/captain. |
| `internal/testsupport` | testcontainers Postgres/Redis for integration tests. |
| `internal/users` | User account domain. |
| `internal/version` | Build version. |
| `internal/webdist` | Serves the embedded SPA (`embed_spa` tag). |
| `internal/ws` | Single-goroutine WebSocket hub for live scoreboard. |

### Who imports the infrastructure

- **Docker client** — exactly one file: `internal/runtime/docker.go:19`. Fully contained.
- **MinIO** — exactly one file: `internal/storage/s3.go`. Fully contained.
- **Postgres (`db/gen`)** — 32 files: every domain service, plus `internal/handlers/*`
  (guards.go, mappers.go, admin_*.go) which import `gen` for *types* (`gen.User`,
  `gen.Challenge`), not to run queries — the `Server` holds services, not `*gen.Queries`.
- **Redis** — `cmd/platform/main.go`, `internal/auth/session.go`, `internal/redisx/*`,
  `internal/scoreboard/service.go`, `internal/handlers/handlers.go` (the limiter).

### Are the "strictly enforced internal boundaries" actually enforced?

**No — by convention only.** `.golangci.yml:20-42` enables `errcheck govet staticcheck
ineffassign unused misspell gocritic revive sqlclosecheck bodyclose noctx rowserrcheck
gosec forbidigo godox`. There is **no `depguard`, no `go-arch-lint`, no import-graph
linter**. The only import-ish rules are `forbidigo` (bans `fmt.Print*` outside `cmd/`,
`.golangci.yml:27-30`) and `godox` (fails on `TODO/FIXME/XXX`, `:31-35`). Nothing prevents,
say, `handlers` from importing `runtime`'s Docker client or a service from importing
another service. The boundaries hold today because the authors kept them, not because a
tool checks them.

---

## 2. PURITY OF DECISION LOGIC

### The instance scheduler (`internal/scheduler/scheduler.go`)

Entry points:

```go
func (s *Scheduler) Start(ctx, actorID, teamID, challengeID uuid.UUID) (runtime.Instance, bool, error) // :59
func (s *Scheduler) Stop(ctx, actorID, teamID, challengeID uuid.UUID) error                            // :121
func (s *Scheduler) Extend(ctx, teamID, challengeID uuid.UUID) (runtime.Instance, error)               // :141
func (s *Scheduler) RunExpiry(ctx)                                                                       // :175 (ticker loop)
func (s *Scheduler) ExpireOnce(ctx) error                                                                // :195 (one pass, exported for tests)
func (s *Scheduler) CleanupEnded(ctx) error                                                              // :216
```

- **Decisions and side effects are in the same function.** `Start` (`:59-118`) validates
  visibility/kind/phase/quota *and* generates the flag, deploys the container, writes
  audit, and bumps metrics — one function, no pure core. `ExpireOnce` (`:195-212`) queries,
  destroys containers, audits, and increments metrics inline.
- **time / rand / env inside them:** the clock is injected — `s.clock()` at `:152` (Extend),
  `:198` (ExpireOnce), `:255` (ttlFor). No direct `time.Now`. The only wall-clock is
  `time.NewTicker(30 * time.Second)` at `:176`. **No `time.Sleep`, no `rand`, no
  `os.Getenv`.** The per-instance flag uses `crypto/rand` but inside `flags.New`
  (`internal/flags/flags.go:35`), not in the scheduler.
- **Testable without Docker?** Yes — via `runtime.NewFakeRuntime` (the scheduler tests do
  this, `scheduler_integration_test.go:46`). **Testable without Postgres? No** — the
  scheduler holds `q *gen.Queries` (`:40`) and calls it directly (`GetChallengeByID :63`,
  `SetInstanceExpiry :166`, `ListExpiredInstances :199`). Every scheduler test is a
  testcontainers test. There is no pure decision layer that can be unit-tested against
  fixtures.
- Good bit: `ExpireOnce` is exported specifically so a test can drive one pass with an
  injected clock (`:193-194`) — the expiry math *is* deterministic under test.

### The reconcile loop

Entry points:

```go
func (d *DockerRuntime) Reconcile(ctx) error   // internal/runtime/docker.go:260  (the real logic)
func (m *Manager) Reconcile(ctx) error          // internal/runtime/manager.go:232 (one-line delegate)
func runReconcile(ctx, log, rt)                 // cmd/platform/main.go:365 (60s ticker)
```

- **Decisions and side effects in the same function.** `Reconcile` (`docker.go:260-305`)
  lists containers, reads DB rows, and mutates both — `UpdateInstance(...StateLost)` at
  `:288`, `ContainerRemove` orphans at `:300`, `gcTeamNetworks` at `:303` — with no
  separable "compute the diff" step.
- **time / rand / env:** none directly in `Reconcile`; `Status` (called at `:292`) stamps
  `d.now()` (`docker.go:220`), where `now` is injected (`:64`). The 60s cadence is
  `time.NewTicker` in `main.go:366`.
- **Testable without Docker or Postgres? No.** `Reconcile` calls `d.cli.*` (Docker) and
  `d.q.*` (Postgres). `FakeRuntime.Reconcile` is a **no-op** (`internal/runtime/fake.go`,
  returns nil). So the real reconcile decision logic has **no coverage outside the
  `dockerint`-tagged tests** — and even those don't assert reconcile directly beyond the
  network-GC case (`docker_hardening_integration_test.go`).

### Scoring engines (`internal/scoring/engine.go`)

**Pure today.** `StaticEngine.Value` (`:27`) and `DynamicEngine.Value` (`:38-53`) are pure
functions of `(params, solves)` — no DB, no clock, no I/O. The `scoring` package does not
import `db/gen` (absent from the DB-importer list in §1). The solve *count* is read from the
DB by callers (`submissions.currentPoints`, `challenges.CurrentPoints`) and passed in; the
engine itself never reads state.

---

## 3. DOCKER SURFACE

**Every Docker call lives in `internal/runtime/docker.go`.** Nowhere else touches the
client. Grouped by operation:

| Op | Lines |
|---|---|
| `ContainerCreate` | 144 |
| `ContainerStart` | 148 |
| `ContainerStop` | 180 |
| `ContainerInspect` | 205, 449 |
| `ContainerList` | 261, 466 |
| `ContainerLogs` | 239 |
| `ContainerRemove` | 300, 474 |
| `ImageInspect` / `ImagePull` | 424 / 427 |
| `NetworkList` | 311, 360 |
| `NetworkInspect` | 321, 402 |
| `NetworkCreate` | 391 |
| `NetworkRemove` | 328, 376 |

**Behind an interface?** Yes. Callers use `runtime.ChallengeRuntime` (`runtime.go:69`); the
concrete `*client.Client` is a private field of `DockerRuntime` (`docker.go:46`) and is
**never passed around**. `FakeRuntime` (`fake.go`) is the test double. Good containment.

**Labels set:**
- Containers (`docker.go:120-124`): `osctf.managed=true`, `osctf.challenge_id`,
  `osctf.instance_id`.
- Networks (`docker.go:383-386`): `osctf.managed=true`, plus `osctf.team_network=true`
  for per-team bridges.

**Adoption after restart is by LABEL.** `Reconcile` lists containers filtered by
`osctf.managed=true` (`:261-263`), keys them by the `osctf.instance_id` label into a map
(`:271-276`), then cross-references DB rows (`:283-295`). There is **no in-memory
container map** that survives across calls — the map is rebuilt every pass. Orphan removal
also keys off the label (`:296-302`). So adoption = *label match cross-referenced against
the DB row set*.

**Weakness worth flagging (egress).** `ensureNamedNetwork` no longer uses Docker's
`Internal: true`; it disables `enable_ip_masquerade` (`docker.go:38, 388-390`). The code's
own comment (`:336-358`) is blunt that this is **weaker** than an internal bridge: the
bridge gateway stays addressable so a challenge can reach host services and any *published*
port including the platform's own API, and **Docker Desktop re-NATs so egress is not
blocked there at all**. Team-to-team isolation still holds (unpublished ports on other
bridges are unreachable), but "egress: false" is a Linux-host-only guarantee.

---

## 4. STATE OWNERSHIP

**Instance lifecycle state lives almost entirely in Postgres — the `instances` row is the
single source of truth.** There is no separate "desired vs running" model; one row carries
both.

| State | Where | Notes |
|---|---|---|
| desired + observed state | Postgres `instances.state` | `0001_init.sql:117`; one enum column, no separate desired spec. |
| container id / host port / network | Postgres `instances.*` | `container_id`, `host_port`, `network`. |
| TTL / expiry | Postgres `instances.expires_at` | `0002_dynamic_instances.sql`; extends mutate it (`scheduler.go:166`). |
| per-instance flag | Postgres `instances.flag` | secret; no unique constraint (see §7). |
| quota | derived query | `CountTeamRunningInstances` (`manager.go:210`); not stored. |
| sessions | Redis | `sess:{token}`, `sess:user:{id}` (`auth/session.go:43-44`). |
| rate-limit windows | Redis | `rl:{scope}:{key}` (`redisx/ratelimit.go:27`). |
| scoreboard cache / frozen snapshot | Redis | `scoreboard:current`, `scoreboard:frozen` (`scoreboard/service.go:18-19`). |
| — process memory — | | only `sync.Mutex`es (no state) + WS live connections + WS `lastScoreboard` bytes (`ws/hub.go:55`). |

**Lost on restart:** the WS in-memory `lastScoreboard`/connection set (ephemeral) and the
scoreboard cache if Redis is wiped — both regenerated (the scoreboard is recomputed at boot,
`main.go:279`). Instance rows and sessions survive (Postgres/Redis). Nothing lifecycle-
critical is memory-only.

**Resync-on-boot path:** `main.go:240` runs `rtMgr.Reconcile(ctx)` once at startup. It
reconciles by: (a) marking any DB row with no matching live container as `lost`
(`docker.go:288`); (b) force-removing labelled containers with no DB row (orphans,
`:300`); (c) GC'ing empty per-team networks (`:303`). **It does not re-spawn missing
containers or re-attach networks.** A container that died while the platform was down leaves
its row in `lost` and stays there — recovery requires a participant to Start again (per-team)
or an admin to redeploy (shared). There is no auto-heal.

---

## 5. CONCURRENCY INVENTORY

### Long-lived goroutines (all started in `serve`, `cmd/platform/main.go`)

| Goroutine | Started | Stopped by | Graceful? |
|---|---|---|---|
| `hub.Run` (WS event loop) | `:232` | `ctx.Done` (`ws/hub.go:94`) closes all conns | signalled, **not joined** |
| `runTickers` (freeze/phase/cleanup, 15s) | `:299` | `ctx.Done` (`main.go:335`) | signalled, not joined |
| `runReconcile` (60s) | `:300` | `ctx.Done` (`main.go:370`) | signalled, not joined |
| `sched.RunExpiry` (30s) | `:301` | `ctx.Done` (`scheduler.go:180`) | signalled, not joined |
| HTTP server | `:304` | `srv.Shutdown` 10s (`:316-320`) | **yes, awaited** |
| `readPump` (per WS connection) | `ws/handler.go:35` | conn close / ctx cancel | per-conn |

**Shutdown is graceful for HTTP only.** On signal, `main.go:311-322` awaits
`srv.Shutdown` (10s) and returns. The four background goroutines are cancelled via the
shared `ctx` but **there is no `WaitGroup` joining them** (grep: no `sync.WaitGroup` in the
tree). A reconcile or expiry pass mid-Docker-call at shutdown is context-cancelled, not
awaited — an in-flight `DestroyInstance` can be interrupted, leaving residue (§9).

### Mutexes

- `scheduler.mu` (`scheduler.go:48`) — guards `Start`, `ExpireOnce`, `CleanupEnded`.
- `scoreboard.mu` (`scoreboard/service.go:32`) — serializes `Recompute` compute+write
  (`:50`, `:133`).
- The WS hub uses **no mutex** — its `clients` map is touched only inside the single `Run`
  goroutine (comment `ws/hub.go:54`); channels marshal register/unregister/broadcast. This
  is the cleanest concurrency in the codebase.

### Docker calls while holding a lock — **yes, and it matters**

`scheduler.Start` holds `s.mu` (`:60`) across `s.mgr.DeployForTeam` (`:105`), which calls
`rt.Deploy` — a Docker deploy bounded at **120 s** (`docker.go:31,72`, image pull included).
`ExpireOnce`/`CleanupEnded` hold `s.mu` (`:196`, `:217`) across a loop of
`DestroyInstance` → Docker removes (`:204`, `:224`).

Consequence: **one slow deploy serializes every per-team instance operation platform-wide.**
While team A's container is pulling for up to 120 s, team B's Start, all TTL expiries, and
event-end cleanup block on the same mutex. This is a latency/liveness bug, not a data race —
`-race` will never flag it and no test exercises it.

### Race tooling

`-race` is used in all three Go CI jobs (`ci.yml:53,64,66`). **`-shuffle` is not used
anywhere. `goleak` is not used** (`go.sum` only; no import). And the one dedicated race
test, `TestConcurrentDoubleSolveRace` (`submissions_integration_test.go:83`), **runs in no
CI job** — see §8.

---

## 6. AUTHORIZATION

**There is no authorization middleware.** `sessionMiddleware` (`httpserver/server.go:96`)
only *resolves* a cookie into an identity in the request context; it does not require or
enforce anything. `originCheckMiddleware` (`:93`) is CSRF, not authz. Every authorization
decision is made **per-handler** (guards) and/or **in the service layer** — scattered
across three layers with no single policy point:

- **Role:** `requireAdmin` (`handlers/guards.go:29`) re-reads the user row so ban/role take
  effect immediately; used by every `Admin*` handler.
- **Authn:** `requireUser` (`guards.go:19`) reads the context identity.
- **Event phase:** `eventStarted` (`challenges.go:17`) for the board/detail;
  `scheduler.requireRunning` (`scheduler.go:235`) for instance start; the submit window in
  `submissions.Submit`.
- **Visibility:** in the `challenges` service (`GetVisibleDetail`, `ListVisible`) and
  re-checked in `DownloadAttachment` (`challenges.go:126` block).
- **Ownership:** per-team instance ops scope by the caller's team via
  `GetTeamInstance(challengeID, teamID)` (`scheduler.go:122,142`); team captain checks live
  in the `teams` service.
- **Max attempts:** in the `submissions` service tx (`service.go`, `locked.MaxAttempts`).

### Route → guard map (45 operationIds, all located)

Guarded handlers (`file:line` = handler def, arrow = first guard):

```
AdminListChallenges      admin_challenges.go:20   requireAdmin
AdminCreateChallenge     admin_challenges.go:50   requireAdmin
AdminGetChallenge        admin_challenges.go:114  requireAdmin
AdminUpdateChallenge     admin_challenges.go:129  requireAdmin
AdminDeleteChallenge     admin_challenges.go:213  requireAdmin
AdminUploadAttachment    admin_challenges.go:248  requireAdmin
AdminDeleteAttachment    admin_challenges.go:291  requireAdmin
AdminDeployInstance      admin_instances.go:62    requireAdmin
AdminGetInstance         admin_instances.go:84    requireAdmin
AdminRestartInstance     admin_instances.go:106   requireAdmin
AdminDestroyInstance     admin_instances.go:127   requireAdmin
AdminGetInstanceLogs     admin_instances.go:143   requireAdmin
AdminListSubmissions     admin_submissions.go:13  requireAdmin
AdminGetStats            admin_submissions.go:65  requireAdmin
AdminListTeams           admin_teams.go:12        requireAdmin
AdminUpdateTeam          admin_teams.go:38        requireAdmin
AdminListUsers           admin_users.go:13        requireAdmin
AdminUpdateUser          admin_users.go:42        requireAdmin
AdminResetPassword       admin_users.go:70        requireAdmin
AdminGetEvent            events.go:29             requireAdmin
AdminUpdateEvent         events.go:45             requireAdmin
AdminListInstances       instances_scheduler.go:129  requireAdmin
AdminDestroyInstanceById instances_scheduler.go:148  requireAdmin
Logout / GetMe / ChangePassword  auth.go:165/178/195  auth.IdentityFrom
ListChallenges / GetChallenge / DownloadAttachment  challenges.go:34/61/126  requireUser (+eventStarted)
StartInstance/StopInstance/ExtendInstance  instances_scheduler.go:66/90/109  requireScheduler→callerTeam
SubmitFlag               submissions.go:15       requireUser
CreateTeam/JoinTeam/LeaveTeam/RenameTeam/RegenerateInviteCode  teams.go:13/30/47/125/145  requireUser
GetScoreboard            scoreboard.go:12        callerIsAdmin (freeze visibility)
GetUser                  users.go:13             callerIsAdmin (enrichment branch; public base)
```

Intentionally **public (no guard, verified)**: `Register` (`auth.go:98`), `Login`
(`auth.go:127`), `GetEvent` (`events.go:13`), `ListTeams` (`teams.go:61`), `GetTeam`
(`teams.go:79`).

**Every route's check is locatable — none missing today.** But the map is held by
discipline, not structure. Because authz is per-handler with no middleware and no test that
enumerates routes, **a newly added admin route that forgets `requireAdmin` would be silently
public**, and nothing (compiler, lint, middleware, or test) would catch it.

---

## 7. FLAG HANDLING

### DB → participant response

- Storage: `challenges.flag` (`gen.Challenge`) and `instances.flag` (`gen.Instance`).
- **Participant DTOs are separate types, not the internal model.** `apigen.Challenge` and
  `apigen.ChallengeDetail` have **no `Flag` field** (only `FlagMode`). The admin
  `apigen.ChallengeAdmin` **does** have `Flag string json:"flag"`. So flag protection is by
  **type separation**, not `json:"-"` stripping.
- The only place a stored flag is copied into a DTO is `toChallengeAdmin`
  (`handlers/challenge_mappers.go:64`, admin-only). The participant mappers (`toChallenge`,
  `toChallengeDetail`) never touch `.Flag`.

### Every place a flag value is read / compared / rendered

- Compared (constant-time): `compareFlag` via `subtle.ConstantTimeCompare`
  (`submissions/service.go:229`), called at `:126` (static) and `:193` (per-instance);
  cross-team lookup `FindInstanceByFlag` (`:197`).
- Read/injected as `FLAG` env: `scheduler.go:96` (`ch.Flag` or generated), threaded to
  `manager.buildSpec` → `decodeEnv` which sets `env["FLAG"]` (`manager.go:409`).
- Rendered: admin only (`challenge_mappers.go:64`). **Never logged** — grep for a flag value
  reaching a logger is clean; audit meta carries ids only (and a test guards it, below).

### Per-instance flag generation & "uniqueness"

`flags.New` (`internal/flags/flags.go:33-38`): 24 bytes `crypto/rand` (192 bits), Crockford
base32, `osctf{…}`. Injected before deploy; stored on `instances.flag`
(`manager.go:324-326`). **Uniqueness is entropy-only.** `instances.flag` has **no unique
index** (only `uq_instances_shared`, `uq_instances_per_team`, `uq_instances_host_port` in
`0002_dynamic_instances.sql:31-32` and `0001_init.sql:126-127`), and `flags.New` performs
**no collision check** against existing rows. The package comment says "mints a unique flag"
(`flags.go:31`) — accurate only in the statistical sense; nothing enforces it.

### Tests asserting a flag can't appear in a participant response — **yes, they exist**

- Participant detail: `challenges_integration_test.go:131-132` (`raw["flag"]` absent).
- Board: `challenges_integration_test.go:148-149`.
- Instance payload: `instances_scheduler_integration_test.go:81-82` (substring `"flag"`
  absent — brittle; passes only because `TeamInstance` omits the field entirely).
- Admin fleet: `instances_scheduler_integration_test.go:126-127` (no `osctf{`).
- Audit meta: `submissions/service_perinstance_integration_test.go:190`.

Caveat: these are top-level-key / substring checks. They would **not** catch a flag exposed
inside a nested object added later, and — critically — several of them are in files that do
not run in CI (§8).

---

## 8. TEST INVENTORY

### Go tests

| File | Tier | ~asserts | funcs |
|---|---|---|---|
| `auth/password_test.go` | unit | PHC round-trip, dummy-hash timing | 5 |
| `config/config_test.go` | unit | env defaults + validation | 7 |
| `flags/flags_test.go` | unit | flag shape/prefix/uniqueness-in-loop | 3 |
| `scoring/engine_test.go` | unit | static/dynamic value vectors | 5 |
| `seed/challenge_yaml_test.go` | unit | yaml parse + per_team validation | 3 |
| `auth/session_integration_test.go` | testcontainers | session set/get/revoke | 1 |
| `handlers/auth_integration_test.go` | testcontainers | register→login→me→logout | 1 |
| `handlers/teams_integration_test.go` | testcontainers | team lifecycle | 1 |
| `handlers/admin_integration_test.go` | testcontainers | admin CRUD, ban | 2 |
| `handlers/challenges_integration_test.go` | testcontainers | visibility, flag-leak, attachments | 3 |
| `handlers/instances_integration_test.go` | testcontainers | shared deploy (fake), reject-standard | 2 |
| `handlers/instances_scheduler_integration_test.go` | testcontainers | per-team endpoints, quota, authoring 422 | 3 |
| `handlers/submissions_integration_test.go` | testcontainers | double-solve race, dynamic scoring, freeze | 3 |
| `handlers/ws_integration_test.go` | testcontainers | live scoreboard push | 1 |
| `runtime/manager_integration_test.go` | testcontainers | hardened spec, per-team isolation/quota | 2 |
| `scheduler/scheduler_integration_test.go` | testcontainers | quota/idempotent/extend/expiry/cleanup | 5 |
| `submissions/service_perinstance_integration_test.go` | testcontainers | per-instance compare + sharing signal | 2 |
| `runtime/docker_integration_test.go` | dockerint | real deploy/logs/destroy/port-reuse | 1 |
| `runtime/docker_hardening_integration_test.go` | dockerint | rootfs/tmpfs, network isolation probe, GC | 4 |

**Counts:** unit **6 files / ~23 funcs**; testcontainers **12 files / ~26 funcs**; dockerint
**2 files / 5 funcs**. Frontend: vitest **4 files** (`ChallengeDialog`, `InstancePanel`,
`lib/time`, `api/client`); playwright **4 specs** (`participant`, `admin`, `freeze`,
`instance`).

### Zero / near-zero coverage (own-package tests)

**No `_test.go` at all** for: `apperr`, `audit`, `challenges`, `clock`, `db`, `events`,
`httpserver`, `httpx`, `metrics`, `pagination`, `redisx`, `scoreboard`, `storage`, `teams`,
`users`, `version`, `webdist`, `ws`. Several are exercised *indirectly* through the handler
testcontainers suite (challenges, teams, users, ws, scoreboard), but as first-class units
they have nothing — notably **`scoreboard` (compute + freeze) and `ws` (fanout/throttle/
drop) have no direct test**, and both have had bugs (§10).

### Skipped / tagged-off / flaky

- **No `t.Skip` anywhere** (grep: 0). Nothing is explicitly disabled.
- `dockerint`-tagged tests (`runtime/docker_*_test.go`) only run when the tag is passed.
- **The real gap — tests that run in NO CI job.** CI: api-test is `go test ./... -short
  -race` (`ci.yml:53`); api-integration is `go test ./... -race -run Integration`
  (`ci.yml:64`). `-short` makes every testcontainers test skip (`testsupport.go:24,59`), so
  api-test runs unit tests only. api-integration selects by **function name matching
  `Integration`** — but five testcontainers funcs are not named that way:
  `TestInstanceLifecycleWithFakeRuntime` (`instances_integration_test.go:29`),
  `TestDeployRejectsStandardChallenge` (`:131`), `TestConcurrentDoubleSolveRace`
  (`submissions_integration_test.go:83`), `TestDynamicScoringAndStandings` (`:141`),
  `TestFreezeBehavior` (`:197`). **These run in neither job.** The mandatory double-solve
  race guard, the dynamic-scoring-standings check, and the freeze-behaviour check are dead
  in CI.
- **Flaky (observed):** the Playwright suite shares one admin account and `login-acct` is
  5 per 5 min (`auth.go:139`); a re-run within the window trips 429 and fails `apiAdmin`
  (`e2e/helpers.ts:20`) — retries are 0. The dockerint isolation probe
  (`docker_hardening_integration_test.go`) is environment-sensitive: the egress weakening
  (§3) means "egress: false" isn't blocked on Docker Desktop, so an egress-based assertion
  there behaves differently than on a Linux runner.
- **`-race`** yes (all Go jobs). **`-shuffle`** no. **`goleak`** no.

---

## 9. FAILURE HANDLING

### Deploy: net → image → remove-stale → create → start → record (5+ steps)

`DockerRuntime.Deploy` (`docker.go:71-170`) has a `defer` that, on any non-unavailable
error, marks the row `error` and returns the row (`:76-81`). That is the **only**
compensation — it does not undo Docker side effects. Concretely, if step N fails:

- **ensureNamedNetwork fails** (`:87`) → wrapped unavailable, row stays `pending`; no
  network created (or a half-created one).
- **ensureImage fails** (`:90`) → row `error`; the network created at `:87` **remains**
  (residue until a later `gcTeamNetworks` pass, `:303`).
- **ContainerCreate fails** (`:144`) → row `error`; network remains.
- **ContainerStart fails** (`:148`) → row `error`, **but a created-and-stopped container
  now exists**. It is *not* an orphan (the DB row exists), so `Reconcile` will not remove
  it (`:296-302` only removes labelled containers with no row). It is cleaned only by the
  next `removeByLabel` on redeploy (`:95`) or by `DestroyInstance`. Until then it lingers.
- **UpdateInstance fails** after a healthy start (`:153`, `:163`) → returns error; the
  container is running but the row may not reflect it.

There is **no transactional rollback** across the Docker/DB boundary. The port is the other
leak: `allocateRow` (`manager.go:334`) inserts the `pending` row (reserving `host_port`)
*before* Deploy runs (`manager.go:153-164`); a failed deploy leaves an `error`/`pending`
row that still counts in `ListUsedPorts` (`:335`), so **the port stays reserved until the
row is destroyed** (`DestroyInstance`) or the challenge is deleted (FK cascade). Nothing
sweeps stale `error` rows automatically.

### Teardown

`DestroyInstance` (`manager.go:219-227`): `rt.Destroy` (label-based container removal,
`docker.go:191`) then `DeleteInstance`. If `DeleteInstance` fails after the container is
gone, the row persists pointing at a removed container → next `Reconcile` marks it `lost`
(`docker.go:288`). Reverse-order teardown is *not* explicit; it relies on `removeByLabel`
being idempotent and `Reconcile` mopping up.

---

## 10. BUG HISTORY

`git log --oneline --grep="fix" -i -n 80` returns 13 commits, but only ~6 are actual fixes
(the rest matched "fix" inside a feature body). **Caveat: 30 commits total, squashed into
large milestone commits — git history is a weak breakage signal here.** Grouped:

| Subsystem | Fix commits | Count |
|---|---|---|
| Scoreboard null/`[]` serialization | `4cde8b5` (backend `[]` not null), `5425204` (drop the now-dead web guard) | 2 |
| Runtime / Docker / CI wiring | `2b767e2` (docker socket gid in CI), `63296e3` (fix runtime wiring), `d579627` (golangci action + e2e stabilize) | 3 |
| Challenges / DB defaults | `3714c75` ("fix CreateChallenge defaults") | 1 |

Empirically, breakage clusters at **serialization boundaries** (the `null` vs `[]` standings
bug bit twice, on both sides of the wire) and at the **Docker/CI integration seam**. The
`scoreboard` and `runtime` packages are exactly where fixes concentrate — and (§8)
`scoreboard` has no direct test and `runtime`'s reconcile has none outside dockerint.

**TODO/FIXME/XXX/HACK:** none in Go/TS source — `godox` (`.golangci.yml:31-35`) fails the
build on `TODO/FIXME/XXX`, so they cannot land. (`HACK` is not in the godox list, but none
exist.) The only "TODO" text is an intentional in-challenge hint *string* in the
`per-team-web` example, not a code comment.

---

## 11. WHAT YOU WOULD BREAK

Five changes most likely to silently break something, and why nothing catches them:

1. **Add a `NOT NULL` column to `challenges` (or change `CreateChallenge`'s insert) without
   a `COALESCE` default.** Direct `q.CreateChallenge` callers — the test seed helpers
   (`scheduler_integration_test.go`, `manager_integration_test.go`,
   `submissions/..._test.go`) and the seeder — pass zero values and violate the constraint.
   This is precisely what `3714c75` fixed. **Why nothing catches it:** api-test (`-short`)
   skips every testcontainers test, so the break only surfaces in api-integration — and only
   for the tests whose names contain `Integration` (§8). A helper used by a
   non-`Integration`-named test would fail invisibly.

2. **Rename or restructure a container/network label** (`osctf.instance_id`, `osctf.managed`,
   `osctf.team_network`; `docker.go:120-124,383-386`). Reconcile adoption and orphan GC key
   entirely off these strings (`:273`, `:298`, `:318`). A mismatch means surviving
   containers stop being adopted (rows go `lost`) and orphans/networks leak. **Why nothing
   catches it:** the reconcile logic has no coverage outside the `dockerint` tag, which CI
   runs only for `./internal/runtime/...` and which asserts almost nothing about label-based
   re-adoption after a restart.

3. **Collapse the participant DTO onto `gen.Challenge` with `json:"-"` field-stripping**
   (instead of the separate `apigen.Challenge` type). Today the participant type simply has
   no `Flag` field (§7); switch to stripping and any later field addition, or a nested
   struct, re-exposes the flag. **Why nothing catches it:** the leak tests check specific
   top-level keys / substrings (`challenges_integration_test.go:132,149`;
   `instances_scheduler_integration_test.go:81`), and two of the strongest ones sit in
   files whose non-`Integration`-named neighbours already don't run in CI.

4. **Add a new admin route and forget `requireAdmin`.** It compiles, lints, and serves —
   **silently public.** Authz is per-handler with no middleware and no test that enumerates
   routes against expected guards (§6). Nothing in the pipeline asserts "every `/admin/*`
   route requires admin."

5. **Change the scheduler's lock scope or the egress network option.** The mutex is already
   held across a 120 s Docker deploy (§5); tightening or widening it, or making
   `DeployForTeam` reentrant, can deadlock or serialize the platform — and `-race` won't
   see a liveness bug, `-shuffle`/`goleak` aren't run, and `TestConcurrentDoubleSolveRace`
   (the one concurrency guard) doesn't execute in CI. Likewise, flipping the egress option
   back to `Internal: true` (`docker.go:388`) silently voids published host ports (the bug
   the current comment documents at `:336-358`); the only test that would notice is the
   environment-sensitive dockerint isolation probe.

### One-line regression-strategy implications

- **Fix the CI selector first:** rename the five non-`Integration` funcs or switch
  api-integration to a build tag / `-short=false` so they actually run — today the
  double-solve, freeze, and dynamic-scoring guards are decorative.
- Add `depguard` to make the "enforced boundaries" real.
- Add a route-enumeration authz test and a whole-response flag-leak scanner (not top-level
  key checks).
- Give `scoreboard`, `ws`, and the reconcile diff their own unit tests (extract a pure diff
  step for reconcile so it can be tested without Docker).
- Turn on `-shuffle=on` and `goleak` in the Go jobs; add a test that asserts a plugin/Docker
  call is not made while holding the scheduler lock (or restructure so it can't be).
