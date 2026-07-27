# Install & Operate OSCTF

This guide takes you from a clean machine to a running CTF. Every step is
copy-pasteable and non-interactive.

## Requirements

- A Linux or macOS host with **Docker** (Engine 24+) and the **Docker Compose**
  plugin.
- **A dedicated VM is strongly recommended.** OSCTF mounts the host Docker socket
  to run challenge containers, which is **root-equivalent on the host**. Do not
  run events on a machine you care about.
- Sizing: ~2 vCPU / 4 GB for the platform stack at 100 participants. Challenge
  containers are extra — budget `sum(mem_limit_mb)` across deployed challenges.
- Open the challenge host-port range on your firewall (default `30000–30999`).

## 1. Get the code and configure

```bash
git clone <repo> && cd OSCTF
cp .env.example .env
```

Edit `.env` and set at minimum:

- `OSCTF_BASE_URL` — the public origin players use (e.g. `https://ctf.example.com`
  or `http://<server-ip>:8080`). This drives the session-cookie `Secure` flag and
  the CSRF origin check.
- `OSCTF_ADMIN_EMAIL` / `OSCTF_ADMIN_PASSWORD` — the seed admin. **Change the
  password** — the platform logs a loud warning if it is left at the default.

## 2. Build the example challenge images (optional but recommended)

The seeded example challenges of `kind: container` need their images built
locally first (they are not pushed to a registry in v0.1):

```bash
make examples
```

Standard challenges (and the whole platform) work without this step.

## 3. Launch

```bash
docker compose up -d --build
```

This builds the image, starts PostgreSQL/Redis/MinIO, migrates the database, and
seeds the admin account, a placeholder event, and the example challenges. Watch
it come up:

```bash
docker compose ps            # all services 'healthy'
curl -fsS http://localhost:8080/healthz     # -> ok
curl -fsS http://localhost:8080/readyz      # -> {"status":"ready"}
```

Open `OSCTF_BASE_URL` in a browser and log in as the admin.

## 4. Put it behind TLS (recommended for public events)

The default assumes you bring your own reverse proxy or run plain `:8080` on a
LAN. To use the bundled Caddy proxy with automatic HTTPS:

```bash
OSCTF_DOMAIN=ctf.example.com docker compose --profile proxy up -d
```

Set `OSCTF_TRUST_PROXY=true` in `.env` so client IPs are read from
`X-Forwarded-For`. **Never expose `/metrics` through your proxy** — the Caddyfile
in `deploy/caddy/` already blocks it.

## 5. Observability (optional)

```bash
docker compose --profile observability up -d
```

Prometheus scrapes `platform:8080/metrics`; Grafana (anonymous admin) is on
`:3000` with a starter dashboard (request rate/latency, submissions, WS
connections, instances by state).

## Backups

There is no built-in backup in v0.1. During an event, cron these:

```bash
docker compose exec -T postgres pg_dump -U osctf osctf > backup-$(date +%F-%H%M).sql
docker run --rm -v osctf_miniodata:/data -v "$PWD":/backup alpine \
  tar czf /backup/minio-$(date +%F-%H%M).tar.gz -C /data .
```

## Upgrades

```bash
git pull && docker compose up -d --build
```

Migrations run on boot. v0.x ships no destructive migrations; releases call out
any that are.

## Troubleshooting

- **`readyz` is 503** — the body names the failing component (`postgres`,
  `redis`, or `minio`). Check `docker compose logs <service>`.
- **A challenge instance won't deploy** — the admin instance panel shows the
  error verbatim. Common causes: the image isn't built (`make examples`), or the
  Docker socket isn't reachable (see the security note above). Instance actions
  return `503` when the runtime is down; the rest of the platform keeps working.
- **Everyone got logged out after a restart** — Redis holds sessions and has no
  volume by default; a Redis restart clears them. This is acceptable and
  documented; add a Redis volume if you need session durability.
- **Structured logs**: `docker compose logs -f platform` — every line carries a
  `request_id` you can correlate with the `X-Request-Id` header players report.
