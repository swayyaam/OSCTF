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
| `OSCTF_TRUST_PROXY` | `false` | — | Honor X-Forwarded-For / X-Forwarded-Proto (set true behind the caddy profile or any proxy). See **Reverse proxies & client IP** below |
| `OSCTF_WS_MAX_CONNS` | `20000` | — | Global ceiling on live scoreboard WebSocket connections (0 = unlimited) |
| `OSCTF_WS_MAX_CONNS_PER_CLIENT` | `256` | — | Live WebSocket connections per client — per authenticated user, or per IP for anonymous connections (0 = unlimited). Raise for large events with many anonymous viewers behind one NAT |
| `OSCTF_WS_HANDSHAKE_BURST` | `600` | — | WebSocket handshakes per client per window (0 = unlimited) |
| `OSCTF_WS_HANDSHAKE_WINDOW` | `60s` | — | Sliding window for the handshake rate limit |
| `OSCTF_REGISTER_IP_BURST` | `500` | — | Registrations allowed per IP per window (0 = disabled). Generous by default for shared-IP venues ([issue #1](https://github.com/swayam-mishra/OSCTF/issues/1)); tighten for public-internet deployments |
| `OSCTF_REGISTER_IP_WINDOW` | `10m` | — | Sliding window for the register-IP limit |
| `OSCTF_PASSWORD_HASH_CONCURRENCY` | `0` (derive) | — | Max concurrent argon2id hashes (register + login + timing burn). `0` derives from the host memory limit (¼ mem ÷ 64 MiB, clamped 2–64). Peak hashing memory ≈ value × 64 MiB ([issue #3](https://github.com/swayam-mishra/OSCTF/issues/3)) |
| `OSCTF_PASSWORD_HASH_MAX_WAIT` | `5s` | — | Max time a request queues for a hash slot before it is shed with 503 + Retry-After |
| `OSCTF_CORS_DEV_ORIGIN` | *(empty)* | — | Dev only: allow the Vite origin |
| `OSCTF_LOG_FORMAT` | `json` | — | `json` \| `text` |
| `OSCTF_LOG_LEVEL` | `info` | — | `debug` \| `info` \| `warn` \| `error` |

Config parse failure = process exit listing every missing/invalid var at once (not first-error-only).

## Reverse proxies & client IP (shared-IP / NAT events)

The scoreboard WebSocket is public and unauthenticated. Admission control (the `OSCTF_WS_*`
caps above and the per-IP rate limits on register/login) keys on the **authenticated user**
where a session cookie is present, and falls back to the **client IP** only for anonymous
connections. So a whole campus lab or venue behind one NAT of *logged-in* players is not
squeezed through a single IP budget — each user has their own. Only anonymous scoreboard
viewers behind one IP share a bucket; raise `OSCTF_WS_MAX_CONNS_PER_CLIENT` /
`OSCTF_WS_HANDSHAKE_BURST` if you expect many. (This is the same shared-IP class as
[GitHub issue #1](https://github.com/osctf/platform/issues/1) — the register-IP rate limit.)

**`OSCTF_TRUST_PROXY` cuts both ways — get it right:**

- **Behind a reverse proxy (Caddy/nginx/ALB): set `OSCTF_TRUST_PROXY=true`.** Otherwise every
  request resolves to the *proxy's* IP, and all anonymous clients — the entire event — collapse
  into one shared per-IP bucket and lock each other out. The server logs a one-time warning if it
  sees `X-Forwarded-For` while `OSCTF_TRUST_PROXY` is off.
- **Directly exposed (no proxy): keep `OSCTF_TRUST_PROXY=false`.** With it on and no trusted proxy
  in front, a client can forge `X-Forwarded-For` to dodge its own limit or exhaust another IP's.
  Only enable it when a proxy **you control** overwrites the header.

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
  - **v0.2 → v0.2.1 (per-instance flag exposure — operator action):** v0.2 stored the raw submitted flag in `submissions.provided`, so a per-team instance flag that was submitted (a team's own correct solve, or another team's flag via sharing) was readable through the admin submissions view. v0.2.1 redacts these on write, and migration `0004_redact_historical_provided_flags` backfills existing rows (correct per-instance solves, plus wrong guesses whose value still matches a live instance flag) to `[redacted per-instance flag]` on boot. This is **best-effort**: a shared flag whose instance was already destroyed cannot be matched by value and is left as-is (correct solves are always caught, so a team's own flag is never left exposed). If your event handled sensitive per-instance flags, treat any already-exported `submissions.provided` data as exposed. Genuine wrong guesses are preserved for triage.
- **Sizing**: 100 participants ≈ 2 vCPU / 4 GB for the stack; challenge containers extra (`sum(mem_limit_mb)` is the honest planning number). Document in install guide.
- **Registration/login burst memory (argon2id) — bounded**: each sign-up and each login derives an argon2id hash costing a **measured ~64 MiB (67 MB) and ~27 ms of CPU** per call (memory-hard by design; params `m=64MiB, t=3, p=4`). Concurrent hashing is **capped by a semaphore** (`OSCTF_PASSWORD_HASH_CONCURRENCY`, covering registration hashing, login verification, and the unknown-email timing burn), so a venue thundering-herd — a hundred players signing in from one NAT in the same few seconds, which the per-IP register limit now permits (`OSCTF_REGISTER_IP_*`) — **cannot OOM the box**. The default derives the cap from the host memory limit (cgroup limit, else host RAM: a quarter of memory ÷ 64 MiB, clamped to 2–64), so peak hashing memory is bounded to **`concurrency × 64 MiB`**: ≈**1 GiB on the recommended 2 vCPU / 4 GB** stack (cap 16), ≈512 MiB on 2 GB (cap 8), ≈2 GiB on 8 GB (cap 32) — versus the **~6.5 GB** an unbounded 100-way burst would have allocated before v0.2.1. Requests past the cap queue for up to `OSCTF_PASSWORD_HASH_MAX_WAIT` (default 5s) and are then shed with **503 + Retry-After** rather than allocating; a 100-player burst clears through the 16 slots on a 4 GB box in well under a second, all succeeding. Raise the cap only if you have headroom for `cap × 64 MiB`; lower it on a memory-constrained host. Shed requests surface as 503s on `/auth/register` and `/auth/login` in `osctf_http_requests_total`.
- **File descriptors are the real WS limit (large events)**: each live scoreboard WebSocket is a socket fd, but it is **not** a big memory cost — the scoreboard snapshot is serialized once per broadcast and the same buffer is shared across all connections (each client holds only a slice header, coalesced to the latest), so even 5000 connections on a 1000-team board is well under a gigabyte of hub memory. The binding constraint is fd count: `OSCTF_WS_MAX_CONNS` is **automatically clamped at startup to a quarter-reserved fraction of `RLIMIT_NOFILE`** (headroom for Postgres, Docker, Redis, S3, and HTTP), and the server logs a loud warning when the configured cap exceeds what the ulimit supports. The default soft limit (1024–4096 on most hosts) caps you well below a few thousand WS connections — for a large public scoreboard, **raise the ulimit** (e.g. `LimitNOFILE=65536` in the systemd unit, or `ulimits: nofile:` in compose) and size `OSCTF_WS_MAX_CONNS` accordingly. Without headroom you would otherwise hit `accept: too many open files`, which fails HTTP, DB, and Docker calls together rather than shedding WS load.
- **/metrics** must not be exposed by the operator's proxy (Caddyfile in `deploy/` already excludes it).
- **Docker socket warning** (from [`08-challenge-runtime.md`](08-challenge-runtime.md)) is repeated in the install guide with the dedicated-VM recommendation.
