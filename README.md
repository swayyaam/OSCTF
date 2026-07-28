# OSCTF

> An open, self-hostable platform for cybersecurity competitions, labs, and training.
> **v0.2:** dynamic **per-team challenge instances** — each team gets its own
> isolated container, with its own flag, managed by a built-in scheduler.

CTFs are the entry point; the durable goal is to be the open infrastructure layer that
universities, communities, and companies build their security education on. See
[`docs/project-desc.md`](docs/project-desc.md) for the full vision and roadmap.

## Quick start (golden path)

```bash
git clone <repo> && cd OSCTF
cp .env.example .env          # then edit OSCTF_ADMIN_EMAIL / OSCTF_ADMIN_PASSWORD
docker compose up -d --build
```

Within minutes you have authentication, teams, a challenge board with seeded examples,
flag submission with scoring, a live scoreboard, and an admin panel — at
<http://localhost:8080>. No cloud account, no license key, no external services.

> **Security note:** the platform mounts the host Docker socket to run challenge
> containers, which is **root-equivalent on the host**. Run events on a dedicated VM.
> See [`docs/v0.1/08-challenge-runtime.md`](docs/v0.1/08-challenge-runtime.md).

## Local development

```bash
make setup      # install pinned tools + npm ci
make dev        # start Postgres, Redis, MinIO (compose)
make dev-api    # run the Go API on :8080
make dev-web    # run the Vite dev server on :5173 (proxies /api -> :8080)
```

- API: <http://localhost:8080>
- Web (dev): <http://localhost:5173>
- MinIO console (dev): <http://localhost:9001>

For everything else — architecture, how to add an endpoint, how to author a challenge —
read [`AGENTS.md`](AGENTS.md) and [`docs/`](docs/).

## Layout

| Path | What |
|---|---|
| `api/` | Go backend (modular monolith): HTTP, services, stores, runtime |
| `dashboard/` | React + TypeScript SPA (Vite) |
| `examples/` | Seeded example challenges (`challenge.yaml` format) |
| `deploy/` | Prometheus/Grafana/Caddy configs (optional compose profiles) |
| `docs/` | The build specification (`docs/v0.1/`) and vision (`docs/project-desc.md`) |
| `scripts/` | Smoke test and dev helpers |

## License

[Apache License 2.0](LICENSE). Contributions are accepted under the same license
(see [`NOTICE`](NOTICE)).

## Per-team instances (v0.2)

Mark a `container` challenge `per_team` in the admin editor; participants then click
**Start** to get their own container (own port, network-isolated, optional per-team
unique flag) and **Stop**/**Extend** it. The scheduler expires instances on a TTL
and tears them all down at event end. Tunable via `OSCTF_INSTANCE_TTL`,
`OSCTF_INSTANCE_EXTEND`, `OSCTF_INSTANCE_MAX_TTL`, `OSCTF_TEAM_INSTANCE_QUOTA`, and
`OSCTF_FLAG_PREFIX` (see [`.env.example`](.env.example)). Every challenge container
now runs read-only-rootfs with egress control; per-team instancing needs the wider
`OSCTF_PORT_RANGE` (30000–32767) open on the host. Full spec: [`docs/v0.2/`](docs/v0.2/README.md).

## Status

v0.2, feature-complete. No API stability promises before v1.0.
