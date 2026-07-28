# 08 — Deployment & Configuration

v0.2 deploys exactly like v0.1 — one `platform` binary + Postgres + Redis + MinIO via
`docker compose`, Docker socket mounted for the runtime. No new services. The changes are
**new env vars** (all with safe defaults) and a **wider default port range**. Baseline:
[`../v0.1/10-deployment.md`](../v0.1/10-deployment.md).

## New environment variables

Added to `api/internal/config/config.go` (same `env`/`envDefault` struct-tag pattern) and
documented in `.env.example`:

```go
// --- Instances (v0.2) ---
InstanceTTL       time.Duration `env:"OSCTF_INSTANCE_TTL"        envDefault:"3600s"` // default per-team TTL
InstanceExtend    time.Duration `env:"OSCTF_INSTANCE_EXTEND"     envDefault:"1800s"` // added per Extend
InstanceMaxTTL    time.Duration `env:"OSCTF_INSTANCE_MAX_TTL"    envDefault:"14400s"`// max total lifetime
TeamInstanceQuota int           `env:"OSCTF_TEAM_INSTANCE_QUOTA" envDefault:"3"`     // concurrent per team
FlagPrefix        string        `env:"OSCTF_FLAG_PREFIX"         envDefault:"osctf"` // per-instance flag prefix
```

The port range default **widens** (keep in lock-step with the DB `host_port` CHECK,
[`02-database.md`](02-database.md)):

```go
PortRangeStart int `env:"OSCTF_PORT_RANGE_START" envDefault:"30000"` // unchanged
PortRangeEnd   int `env:"OSCTF_PORT_RANGE_END"   envDefault:"32767"` // was 30999
```

### Validation (extend `config.Validate`)

- `OSCTF_INSTANCE_TTL >= 0`, `OSCTF_INSTANCE_EXTEND > 0`, `OSCTF_INSTANCE_MAX_TTL >=
  OSCTF_INSTANCE_TTL` (and `>= OSCTF_INSTANCE_EXTEND`).
- `OSCTF_TEAM_INSTANCE_QUOTA >= 1`.
- Existing rule `1 <= PortRangeStart <= PortRangeEnd <= 65535` still applies; if the range
  is narrower than `quota × expected-teams` the platform still runs (Start returns the v0.1
  "no free challenge ports" `409` when exhausted) — a `log.Warn` at boot when the range is
  smaller than `quota × 32` nudges operators to widen it.

## `.env.example` additions

Append a section (mirroring the existing layout):

```dotenv
# --- Instances (v0.2) ---------------------------------------------------------
# Per-team challenge instances: default time-to-live, extend step, and max lifetime.
OSCTF_INSTANCE_TTL=3600s
OSCTF_INSTANCE_EXTEND=1800s
OSCTF_INSTANCE_MAX_TTL=14400s
# Max concurrent running instances one team may hold across all challenges.
OSCTF_TEAM_INSTANCE_QUOTA=3
# Prefix for generated per-instance flags: osctf{...}. Match your event brand if desired.
OSCTF_FLAG_PREFIX=osctf

# Widened for per-team instancing (many teams x quota). Keep >= expected concurrent instances.
OSCTF_PORT_RANGE_START=30000
OSCTF_PORT_RANGE_END=32767
```

## Compose changes

Minimal:

- **Published port range** in `docker-compose.yml` must cover the widened range. If the
  compose file publishes a fixed range for challenge containers, update it to
  `30000-32767`. (The platform binds host ports itself via the Docker API; the compose
  change is only relevant where a range is pre-declared/firewalled.)
- **Firewall / host:** operators exposing challenge ports must open `30000–32767` (was
  `30000–30999`). Call this out in the upgrade notes.
- No new container, volume, or dependency. Postgres/Redis/MinIO unchanged.

## Upgrade path from v0.1

1. Pull v0.2 image / build the binary.
2. `platform migrate up` runs `0002_dynamic_instances` — additive, non-destructive; existing
   challenges become `shared`/`static` (identical behaviour).
3. Set the new env vars (or accept defaults) and widen the firewall range.
4. Existing shared/static challenges and running shared instances keep working. No data
   migration, no downtime beyond the normal restart.

Downgrade (v0.2 → v0.1) requires draining per-team instances first (the `0002` down
migration restores the one-instance-per-challenge unique constraint) — see
[`02-database.md`](02-database.md). Instances are cattle; the scheduler's event-end cleanup
or a manual admin destroy drains them.

## Sizing

Per-team instancing multiplies container count: worst case `teams × quota` concurrent
containers, each bounded by the challenge's `mem_limit_mb` / `cpu_millis` (v0.1 defaults 256
MiB / 0.5 CPU). Plan host capacity as:

```
peak_containers ≈ min(teams × quota, distinct_per_team_challenges × teams)
peak_mem        ≈ peak_containers × avg mem_limit_mb
host_ports_used ≈ peak_containers   (must fit in OSCTF_PORT_RANGE)
```

- A 50-team event, quota 3, 256 MiB each → up to 150 containers, ~38 GiB — size the host or
  lower quota / mem limits accordingly.
- Per-team **networks**: one bridge per team with a live instance (auto-GC'd). Docker's
  default bridge address pool is finite; a very large event (hundreds of simultaneous team
  networks) may need `default-address-pools` tuning in `daemon.json` — documented as an
  operational note, not configured by the platform.
- The scheduler and tickers add negligible load (a few queries every 15–30 s).

## Security notes (delta)

- **Egress:** challenges with `egress: false` get an `--internal` per-team network (no
  outbound). Default is egress-on (v0.1 parity). Operators running untrusted user code
  should prefer `egress: false` unless the task needs the network.
- **Read-only rootfs + tmpfs** is now the default for every deployed container — a smaller
  blast radius than v0.1. The Docker-socket trust boundary is unchanged (restated in
  [`03-runtime.md`](03-runtime.md)).
- No secrets added to config beyond the flag *prefix* (not a secret). Per-instance flags
  live only in the DB and the container env.

## Decision log

- **Widen default port range to 30000–32767.** Per-team instancing needs headroom; 2768
  ports covers realistic self-hosted events. Kept in sync with the DB CHECK.
- **All new instance knobs have defaults; none are required.** A v0.1 `.env` upgrades with
  zero edits and gets sensible TTL/quota behaviour.
- **No new services.** The scheduler is in-process; there is nothing new to run or monitor
  beyond the existing `/metrics` counters.
