# 03 — Tech Stack & Coding Conventions

Choices are **locked for v0.1**. "Boring and durable" beats "interesting". Versions below are minimums at spec time (2026-07); use the latest patch release of each, pin exactly in `go.mod` / `package-lock.json`, and upgrade only via dedicated PRs.

## Backend (Go)

| Concern | Choice | Notes |
|---|---|---|
| Language | **Go 1.25** | `go.mod` says `go 1.25`; use stdlib-first mindset |
| HTTP router | **chi v5** (`go-chi/chi/v5`) | Boring, middleware-friendly, oapi-codegen supports it natively |
| OpenAPI codegen | **oapi-codegen v2** | `strict-server` mode + chi; types & server interface into `internal/apigen` |
| Request validation | **kin-openapi** validator middleware (comes with oapi-codegen ecosystem) | Rejects schema-invalid requests before handlers |
| Database driver | **pgx v5** (`jackc/pgx/v5`) with `pgxpool` | No ORM. Ever. |
| Query codegen | **sqlc** | Handwritten SQL in `internal/db/queries/*.sql` → typed Go in `internal/db/gen` |
| Migrations | **goose v3** (`pressly/goose/v3`) | Plain-SQL files, embedded via `embed.FS`, run automatically on boot and via `platform migrate` |
| Redis client | **go-redis v9** (`redis/go-redis/v9`) | Sessions, rate limits, scoreboard cache |
| Docker client | **`github.com/docker/docker/client`** | Pin to a recent stable engine API version; negotiate at init |
| Object storage | **minio-go v7** | Works against MinIO and any S3-compatible store |
| WebSocket | **`github.com/coder/websocket`** | Maintained, context-aware, minimal |
| Password hashing | **`golang.org/x/crypto/argon2`** | Parameters in [`06-auth.md`](06-auth.md) |
| UUIDs | **`google/uuid`** | `uuid.NewV7()` everywhere |
| Config | **`caarlos0/env/v11`** | Struct tags → env; no config files, no viper |
| Logging | **stdlib `log/slog`** | JSON handler in prod, text in dev (`OSCTF_LOG_FORMAT`) |
| Metrics | **`prometheus/client_golang`** | `/metrics`; standard Go + process collectors + custom counters below |
| Testing | stdlib `testing` + **testcontainers-go** for integration | No assertion framework; small local `assert` helpers acceptable |
| Lint | **golangci-lint** | Config below |

**Explicitly rejected**: gin/echo/fiber (chi is enough), GORM/ent (sqlc), viper (env only), zap/logrus (slog), asynq/river (no queue in v0.1), wire/fx (manual DI).

### Custom Prometheus metrics (minimum set)

```
osctf_http_requests_total{route,method,status}      counter
osctf_http_request_duration_seconds{route,method}   histogram
osctf_submissions_total{correct}                     counter
osctf_ws_connections                                 gauge
osctf_instances{state}                               gauge
osctf_ratelimit_rejections_total{scope}              counter
```

### Go conventions

- **Errors**: wrap with `fmt.Errorf("deploying instance %s: %w", id, err)`. Sentinel errors per domain package (`var ErrNotFound = errors.New(...)`). Check with `errors.Is/As`. Never return a bare `err` across a package boundary without context.
- **Logging**: only in handlers/middleware/main/tickers — services return errors, they don't log them. Always include `slog` attrs: `request_id`, and where relevant `user_id`, `team_id`, `challenge_id`. Never log flags, passwords, session tokens, or password hashes.
- **Context**: first param everywhere; respect cancellation in loops; DB calls get the request context (tickers get their own with timeout).
- **Transactions**: services own transaction boundaries via a small `db.WithTx(ctx, pool, fn)` helper. A service function is either fully in one tx or does single statements — no half-tx code paths.
- **Time**: inject `func() time.Time` (a `Clock` field defaulting to `time.Now`) into services that make time-based decisions (event window, freeze). Tests set it. Never call `time.Now()` inside scoring or window checks directly.
- **Package docs**: every package has a doc comment stating its responsibility and what it must not import.
- **No global state** except the metrics registry. No `init()` beyond metric registration.

### `.golangci.yml` (enable at minimum)

`errcheck, govet, staticcheck, ineffassign, unused, misspell, gocritic, revive, sqlclosecheck, bodyclose, noctx, rowserrcheck, gosec` — with `gosec` exceptions documented inline where needed (e.g. G404 in non-crypto contexts). `forbidigo` rule banning `fmt.Print*` outside `cmd/` and banning `FIXME`.

## Frontend (TypeScript)

| Concern | Choice | Notes |
|---|---|---|
| Build | **Vite 6** + **React 19** + **TypeScript 5.x strict** | `strict: true`, `noUncheckedIndexedAccess: true` |
| Routing | **React Router v7** (library mode) | Boring, largest mindshare |
| Server state | **TanStack Query v5** | All API data through it; no manual fetch-in-useEffect |
| API client | **openapi-typescript** (types) + **openapi-fetch** (client) | Generated from `api/openapi/openapi.yaml` into `src/api/schema.d.ts`; committed |
| Styling | **Tailwind CSS v4** | Design tokens in [`09-frontend.md`](09-frontend.md) |
| Components | **shadcn/ui pattern** (vendored Radix-based components in `src/components/ui/`) | Copy in only what's used; no component mega-library dependency |
| Forms | **react-hook-form** + **zod** resolvers | Zod schemas mirror API validation messages |
| Markdown | **react-markdown** + **rehype-sanitize** | Challenge descriptions are markdown; sanitize is non-negotiable |
| Local state | React state/context only | No Redux/Zustand at this scale |
| Tests | **Vitest** + **Testing Library**; **Playwright** for 3 e2e flows | See [`11-testing-ci.md`](11-testing-ci.md) |
| Lint/format | **ESLint 9 (flat config)** + **Prettier** | typescript-eslint strict-type-checked preset |
| Node | **Node 22 LTS** | `.nvmrc` at `dashboard/` |

### TypeScript conventions

- API types come **only** from the generated `schema.d.ts` — never hand-write a type for an API payload.
- Components: function components, props typed inline or with a `Props` interface directly above; no `React.FC`.
- Data fetching: one `src/api/client.ts` exporting a configured `openapi-fetch` client + query-key factory; hooks in `src/api/hooks/` per resource (`useChallenges`, `useSubmitFlag`, …).
- Errors: a single `ApiError` normalizer converts problem+json into `{ title, detail, fields }`; forms map `fields` onto inputs.
- Accessibility: interactive elements are real `<button>/<a>`; every form input has a label; modals trap focus (Radix gives this for free).

## Toolchain pinning

`make setup` installs exact versions (update the pin, not the docs, when bumping):

```
go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.<latest>
go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.<latest>
go install github.com/pressly/goose/v3/cmd/goose@v3.<latest>
go install github.com/daveshanley/vacuum@v0.<latest>          # OpenAPI linter
golangci-lint via its install script, version pinned in Makefile variable
```

CI uses the same Makefile targets, so tool versions live in exactly one place (the Makefile).
