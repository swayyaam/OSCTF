# 10 — Deployment

## Production image

One image, multi-stage `Dockerfile` at the repo root:

1. **Stage `web`**: `node:22-alpine` → `npm ci && npm run build` in `dashboard/` → `dashboard/dist`.
2. **Stage `build`**: `golang:1.25-alpine` → copy `api/`, copy `dashboard/dist` → `api/internal/webdist/static/` → `CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=<git describe>" -o /platform ./cmd/platform`.
3. **Stage `runtime`**: `gcr.io/distroless/static-debian12:nonroot`? **No** — the runtime needs nothing from the OS, but debugging an event at 2 a.m. does: use `alpine:3.20` + `ca-certificates` + the binary, run as non-root user `10001` (the Docker socket group is granted via compose `group_add`). Entrypoint `/platform`, default cmd `serve`.

Image name: `ghcr.io/osctf/platform:<version>` and `:latest` on main. Example challenge images build separately (see [`13-example-challenges.md`](13-example-challenges.md)) as `osctf/example-<slug>:0.1`, built locally by `make examples` — not pushed to a registry in v0.1.

## docker-compose.yml (golden path — spec, not listing)

Services:

| Service | Image | Notes |
|---|---|---|
| `platform` | built from repo (`build: .`) or `ghcr.io/osctf/platform` | ports `8080:8080`; mounts `/var/run/docker.sock`; `group_add: [<docker gid>]` via env; `env_file: .env`; depends_on with `condition: service_healthy` on the three below; healthcheck `wget -qO- localhost:8080/healthz` |
| `postgres` | `postgres:17-alpine` | volume `pgdata`; healthcheck `pg_isready`; **no host port** in prod file (dev override adds 5432) |
| `redis` | `redis:7-alpine` | `--maxmemory 256mb --maxmemory-policy noeviction`; volume optional (sessions are re-creatable; default: no volume, document that a platform restart with Redis loss logs everyone out — acceptable) |
| `minio` | `minio/minio` | `server /data --console-address :9001`; volume `miniodata`; no host ports in prod (console exposed in dev override) |

Profiles:
- `observability`: `prometheus` (scrapes `platform:8080/metrics`, config `deploy/prometheus/prometheus.yml`) + `grafana` (provisioned datasource + one starter dashboard: request rate/latency, submissions, WS connections, instances by state). `docker compose --profile observability up`.
- `proxy`: `caddy` with `deploy/caddy/Caddyfile` — reverse proxy :80/:443 → platform:8080 with automatic HTTPS when `OSCTF_DOMAIN` is set. Optional; the default assumes operators bring their own proxy or run plain :8080 on a LAN.

Networks: default compose network for platform↔stores; the `osctf-challenges` bridge is created by the runtime itself (challenge containers are **siblings** of the compose stack — started via the socket — so they are not compose services and never on the compose network).

Named volumes: `pgdata`, `miniodata`.

`docker-compose.dev.yml` (override): exposes 5432/6379/9000/9001 on localhost, removes the `platform` service (you run it via `make dev-api`), sets postgres to `POSTGRES_PASSWORD=dev`.

## Environment variables (complete reference — also the content of `.env.example`)

| Var | Default | Req | Description |
|---|---|---|---|
| `OSCTF_BASE_URL` | `http://localhost:8080` | ✔ | Public origin; drives cookie Secure flag, Origin check, `{host}` in connection templates |
| `OSCTF_PUBLIC_HOST` | host of BASE_URL | — | Override host used in challenge connection info (e.g. a separate IP for challenges) |
| `OSCTF_HTTP_ADDR` | `:8080` | — | Listen address |
| `OSCTF_DATABASE_URL` | compose-internal DSN | ✔ | `postgres://osctf:...@postgres:5432/osctf?sslmode=disable` |
| `OSCTF_REDIS_URL` | `redis://redis:6379/0` | ✔ | |
| `OSCTF_S3_ENDPOINT` | `minio:9000` | ✔ | |
| `OSCTF_S3_ACCESS_KEY` / `OSCTF_S3_SECRET_KEY` | dev defaults | ✔ | Compose passes the same values to the minio service |
| `OSCTF_S3_BUCKET` | `osctf` | — | Created on boot if missing |
| `OSCTF_S3_USE_SSL` | `false` | — | |
| `OSCTF_ADMIN_EMAIL` / `OSCTF_ADMIN_PASSWORD` | — | ✔ first boot | Seed admin credentials; warn loudly if password is the `.env.example` value |
| `OSCTF_SESSION_TTL` | `168h` | — | |
| `OSCTF_REGISTRATION_OPEN` | `true` | — | |
| `OSCTF_TEAM_MAX_SIZE` | `4` | — | |
| `OSCTF_MAX_ATTACHMENT_MB` | `100` | — | |
| `OSCTF_PORT_RANGE_START` / `OSCTF_PORT_RANGE_END` | `30000` / `30999` | — | Challenge host ports — **must be reachable/open on the host firewall**; the install guide says so |
| `OSCTF_DOCKER_HOST` | *(empty = SDK default)* | — | Alternate docker endpoint |
| `OSCTF_SEED_EXAMPLES` | `true` | — | Seed the 8 example challenges on first boot |
| `OSCTF_TRUST_PROXY` | `false` | — | Honor X-Forwarded-For / X-Forwarded-Proto (set true behind the caddy profile or any proxy) |
| `OSCTF_CORS_DEV_ORIGIN` | *(empty)* | — | Dev only: allow the Vite origin |
| `OSCTF_LOG_FORMAT` | `json` | — | `json` \| `text` |
| `OSCTF_LOG_LEVEL` | `info` | — | `debug` \| `info` \| `warn` \| `error` |

Config parse failure = process exit listing every missing/invalid var at once (not first-error-only).

## First boot & seeding

`platform serve` (and `platform seed`, which does only steps 3–5):

1. Migrate (goose up, embedded).
2. Ensure S3 bucket.
3. Seed admin from env if no user with that email exists (`hidden=true`, `role=admin`).
4. If no event row exists: create one — name "My CTF", starts 7 days from boot, ends 9 days from boot, description pointing at the admin panel. (Everything is placeholder; the admin edits it. A future-dated start means a fresh install never looks "live".)
5. If `OSCTF_SEED_EXAMPLES` and no challenges exist: load each `examples/challenges/*/challenge.yaml` (parser per [`13-example-challenges.md`](13-example-challenges.md)), create challenges `visible=true`, upload their `files/` as attachments. Container-kind examples are created but **not deployed** (deploying requires images built via `make examples`; the admin panel deploy button handles it — the guide walks through it).

All seeding is idempotent (checks before writes) and logged.

## Operations

- **Backups**: `docs/guides/install.md` documents `docker compose exec postgres pg_dump -U osctf osctf > backup.sql` + MinIO volume copy; recommend a cron during events. No built-in backup in v0.1.
- **Upgrades**: `git pull && docker compose up -d --build` — migrations run on boot; releases state when a migration is destructive (none should be in v0.x).
- **Sizing**: 100 participants ≈ 2 vCPU / 4 GB for the stack; challenge containers extra (`sum(mem_limit_mb)` is the honest planning number). Document in install guide.
- **/metrics** must not be exposed by the operator's proxy (Caddyfile in `deploy/` already excludes it).
- **Docker socket warning** (from [`08-challenge-runtime.md`](08-challenge-runtime.md)) is repeated in the install guide with the dedicated-VM recommendation.
