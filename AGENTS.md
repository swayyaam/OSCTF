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

## Gotchas

- **Regenerate after editing `openapi.yaml` or the SQL schema**, or the build breaks
  (`make generate`). The generated code is the contract handlers compile against.
- The dev build serves an SPA placeholder — that's expected. The real SPA is embedded
  only in `-tags embed_spa` builds (the Docker image); in dev use `make dev-web`.
- `make dev-api` requires the dev datastores (`make dev`) to be up first.
- The platform mounts the Docker socket in production — **root-equivalent on the host**.
  Run events on a dedicated VM.
