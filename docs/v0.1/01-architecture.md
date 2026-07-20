# 01 — Architecture

## Shape: modular monolith

One Go process (`platform serve`) contains all backend services. The logical services from the vision doc (auth, events, challenges, teams, deployment) are **packages with enforced boundaries**, not network services. They split into separate deployables only when scale demands it (Phase 4); the boundaries exist now so that split is a refactor, not a rewrite.

```
                    Browser (React SPA)          curl / scripts / agents
                          │                              │
                          └──────────────┬───────────────┘
                                         │  HTTPS (operator's proxy) → HTTP :8080
                              ┌──────────▼──────────┐
                              │   platform serve     │  single Go binary
                              │ ┌──────────────────┐ │
                              │ │ HTTP router (chi)│ │  /api/v0/*  /healthz  /metrics
                              │ │  + middleware    │ │  /* → embedded SPA
                              │ ├──────────────────┤ │
                              │ │ generated handler│ │  oapi-codegen strict server
                              │ │ layer (transport)│ │
                              │ ├──────────────────┤ │
                              │ │ service layer    │ │  auth teams events challenges
                              │ │ (business rules) │ │  submissions scoring scoreboard
                              │ ├──────────────────┤ │
                              │ │ store layer      │ │  sqlc-generated queries (pgx)
                              │ └──────────────────┘ │
                              │  ws hub   runtime    │  scoreboard fan-out; Docker client
                              └───┬────┬────┬────┬───┘
                                  │    │    │    │
                             Postgres Redis MinIO Docker socket
                                                    │
                                          challenge containers
                                          (bridge net, port range)
```

## Package layout and dependency rules

All backend code lives in the single Go module at `api/`. Packages under `api/internal/`:

| Package | Responsibility | May import |
|---|---|---|
| `config` | Env parsing into one typed `Config` struct | stdlib only |
| `db` | pgx pool, goose migrations (embedded), sqlc-generated code in `db/gen` | `config` |
| `httpserver` | Router assembly, middleware (request ID, logging, recovery, CORS, origin check, rate limit, session), SPA static serving | everything below it |
| `apigen` | oapi-codegen output (server interfaces + types). Generated, checked in | — |
| `handlers` | Implements `apigen` strict-server interface; maps HTTP ↔ service calls; no business logic | services, `apigen`, `httpx` |
| `auth` | Passwords (argon2id), sessions (Redis), `AuthProvider` interface + `EmailPasswordProvider` | `db`, `redisx` |
| `users`, `teams`, `events`, `challenges`, `submissions` | Domain services: validation, permissions, transactions | `db`, own domain siblings' interfaces only |
| `scoring` | `ScoringEngine` interface + `StaticEngine`, `DynamicEngine` | stdlib only (pure functions) |
| `scoreboard` | Standings computation, Redis cache, freeze logic | `db`, `scoring`, `redisx` |
| `ws` | WebSocket hub: connection registry, broadcast, per-conn send queues | `scoreboard` (types only) |
| `runtime` | `ChallengeRuntime` interface + `DockerRuntime`; instance lifecycle & reconciliation | `db`, Docker SDK |
| `storage` | `ObjectStore` interface + `S3Store` (MinIO) | MinIO SDK |
| `audit` | Audit log writes | `db` |
| `seed` | First-boot admin + example-challenge seeding | services |
| `httpx`, `redisx` | Shared helpers: problem+json rendering, rate limiter, Redis client setup | stdlib, go-redis |

**Hard rules** (enforce in review; a lint exception list is acceptable):

1. `handlers` never touches `db` directly — always through a service.
2. Services never import `handlers`, `apigen`, or anything HTTP. Services return domain errors; handlers translate them to problem+json.
3. Nothing imports a concrete implementation of the four core interfaces except `main.go` (composition root) and the implementation's own tests.
4. `scoring` is pure: no I/O, no clock, no imports beyond stdlib. It takes numbers, returns numbers.
5. All cross-package calls take `context.Context` as the first parameter.

## The four core interfaces

Defined exactly as below (names, signatures). These are the future plugin surface — changing them later is expensive, so they are deliberately minimal.

```go
// api/internal/auth/provider.go
// AuthProvider authenticates credentials and yields a platform user identity.
// v0.1 implementation: EmailPasswordProvider. Future: OAuth, LDAP, SAML plugins.
type AuthProvider interface {
    // Name returns a stable identifier, e.g. "email".
    Name() string
    // Authenticate verifies credentials and returns the user ID.
    // Returns ErrInvalidCredentials on any failure (no enumeration).
    Authenticate(ctx context.Context, identifier, secret string) (userID uuid.UUID, err error)
}

// api/internal/scoring/engine.go
// ScoringEngine computes a challenge's current point value. Pure.
type ScoringEngine interface {
    Name() string // "static" | "dynamic"
    // Value returns the points awarded to every solver of the challenge
    // given the current number of valid solves (see 07-scoring.md).
    Value(params ChallengeScoring, solves int) int
}

type ChallengeScoring struct {
    Initial int // points_initial
    Min     int // points_min  (dynamic only)
    Decay   int // decay       (dynamic only; solve count at which value reaches Min)
}

// api/internal/runtime/runtime.go
// ChallengeRuntime manages challenge workload containers.
// v0.1 implementation: DockerRuntime. Future: Kubernetes, Podman, Firecracker.
type ChallengeRuntime interface {
    Name() string // "docker"
    // Deploy creates and starts the instance for a challenge. Idempotent:
    // deploying an already-running instance returns it unchanged.
    Deploy(ctx context.Context, spec InstanceSpec) (Instance, error)
    // Stop halts the container but keeps it for restart/log inspection.
    Stop(ctx context.Context, instanceID uuid.UUID) error
    // Destroy stops and removes the container and frees its port.
    Destroy(ctx context.Context, instanceID uuid.UUID) error
    // Status re-inspects the live container and returns current state.
    Status(ctx context.Context, instanceID uuid.UUID) (Instance, error)
    // Logs returns up to tailLines of recent container output.
    Logs(ctx context.Context, instanceID uuid.UUID, tailLines int) (string, error)
    // Reconcile aligns tracked instances with actual runtime state.
    // Called on boot and periodically (see lifecycle section of 08).
    Reconcile(ctx context.Context) error
}

// api/internal/storage/store.go
// ObjectStore persists challenge attachments and future blobs.
// v0.1 implementation: S3Store (MinIO). Interface is S3-shaped on purpose.
type ObjectStore interface {
    Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
    Get(ctx context.Context, key string) (io.ReadCloser, error)
    Delete(ctx context.Context, key string) error
}
```

`InstanceSpec` / `Instance` structs are specified in [`08-challenge-runtime.md`](08-challenge-runtime.md).

## Composition root

`api/cmd/platform/main.go` is the only place that wires concrete implementations:

```go
cfg := config.Load()                    // env → Config, fail fast with named missing vars
pool := db.Connect(cfg)                 // pgx pool; ping with 5s timeout
db.Migrate(pool)                        // goose up, embedded migrations (serve & migrate cmds)
rdb := redisx.Connect(cfg)
store := storage.NewS3Store(cfg)        // ObjectStore
rt := runtime.NewDockerRuntime(cfg, q)  // ChallengeRuntime
eng := scoring.Registry()               // map[string]ScoringEngine{"static":…, "dynamic":…}
// services ← stores; handlers ← services; router ← handlers; serve.
```

No DI framework. Constructor injection, explicit wiring, ~100 lines.

## Key flows (normative)

### Flag submission (the hot path)

1. `POST /api/v0/challenges/{id}/submit` with `{"flag": "..."}`.
2. Middleware: session → user; user must have a team; event must be running (`starts_at <= now < ends_at`). Admins bypass the window but their submissions are marked via their hidden status.
3. Rate limit check (Redis, sliding window): 10/min per `(team, challenge)` and 30/min per user. Over limit → `429` + `Retry-After`.
4. In one DB transaction:
   a. `SELECT ... FOR UPDATE` the challenge (locks it as the solve-count anchor).
   b. Reject if team already solved (`409`), if challenge invisible (`404`), if `max_attempts` exhausted (`403`).
   c. Compare flag: constant-time (`crypto/subtle`), after optional lowercase-both when `flag_case_insensitive`.
   d. Insert the submission row (always — correct or not) with user, team, IP.
5. If correct: recompute standings and write the Redis scoreboard cache (transaction already committed; recompute is read-only), then broadcast `scoreboard.update` through the WS hub.
6. Response: `{"correct": true|false, "points": <current value>|null}`.

The partial unique index `(challenge_id, team_id) WHERE correct` makes double-solves impossible even under concurrent submissions; catch the unique violation and return `409`.

### Scoreboard read

1. `GET /api/v0/scoreboard` → read Redis key `scoreboard:current` (JSON snapshot). On miss, recompute from Postgres, write cache (no TTL; invalidated by writes), return.
2. If `freeze_at` is set and `now >= freeze_at`: non-admin requests are served the `scoreboard:frozen` snapshot, written once at freeze time by the first post-freeze recompute. Admin sessions get live data.
3. WS clients receive the same snapshot payloads push-style; see [`05-api.md`](05-api.md) for the message protocol.

### Instance deploy (admin)

1. `POST /api/v0/admin/challenges/{id}/instance`.
2. Service validates: challenge kind is `container`, image is set.
3. Allocate a host port: `SELECT` smallest free port in `[30000,30999]` not present in `instances`, insert `instances` row `state=pending` (unique constraint on port arbitrates races).
4. Call `ChallengeRuntime.Deploy` (pull image if missing → create container with limits/labels → start → wait for running). Update row to `running` with `container_id`, or `error` with the failure message.
5. Deploy is synchronous with a 120 s timeout (image pulls are slow); the frontend shows a progress state. No job queue in v0.1.

### Boot sequence

`platform serve`: load config → connect PG (retry 10× 3 s — compose start order) → migrate → connect Redis, MinIO (create bucket if missing) → `runtime.Reconcile()` (adopt or mark-lost existing containers by label) → seed on first boot (see [`10-deployment.md`](10-deployment.md)) → start HTTP server + background tickers.

### Background work (no job queue in v0.1)

Plain goroutines with tickers, started by `serve`, stopped via context on shutdown:

| Ticker | Interval | Work |
|---|---|---|
| runtime reconcile | 60 s | `Reconcile()`: re-inspect containers, update instance states |
| scoreboard freeze | 15 s | If freeze time just passed, snapshot `scoreboard:frozen` |
| session sweep | none | Redis TTL handles expiry; nothing to do |

Graceful shutdown: on SIGTERM/SIGINT, stop accepting connections, drain in-flight requests (10 s), close WS connections with code 1001, stop tickers. **Do not** stop challenge containers on shutdown — they outlive the API process by design (reconcile re-adopts them).

## Error taxonomy

Domain errors are sentinel values or typed errors in each service package (`ErrNotFound`, `ErrForbidden`, `ErrConflict`, `ErrValidation{Fields}`, `ErrRateLimited{RetryAfter}`, `ErrEventNotRunning`, …). The single translation point `httpx.RenderError` maps them to problem+json (catalog in [`05-api.md`](05-api.md)). Unknown errors → 500 with a generic body, full detail only in the server log with the request ID.

## What is deliberately absent

- No message queue, no async workers — synchronous paths + tickers cover v0.1.
- No caching layer beyond the scoreboard snapshot and rate-limit counters.
- No soft deletes; deletion rules per table are in [`04-database.md`](04-database.md).
- No feature flags; config is env vars read once at boot.
