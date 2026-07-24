# 08 — Challenge Runtime (Docker)

v0.1 ships one `ChallengeRuntime` implementation: `DockerRuntime`, using the official Docker SDK against the local daemon (`DOCKER_HOST` respected; in compose, the socket is bind-mounted — see security note at the bottom). Scope is **per-event shared instances**: at most one container per `container`-kind challenge (DB-enforced by `uq_instances_challenge`).

## Types

```go
// api/internal/runtime/types.go
type InstanceSpec struct {
    InstanceID   uuid.UUID         // pre-allocated row ID
    ChallengeID  uuid.UUID
    Slug         string            // for names/labels
    Image        string
    InternalPort int               // port the challenge listens on inside the container
    HostPort     int               // pre-allocated from the range (04-database.md)
    MemLimitMB   int
    CPUMillis    int               // 1000 = one core
    Env          map[string]string // challenge-author-provided extras
}

type Instance struct {
    ID           uuid.UUID
    ChallengeID  uuid.UUID
    State        State     // pending starting running unhealthy stopped error lost
    ContainerID  string
    HostPort     int
    StartedAt    *time.Time
    LastHealthAt *time.Time
    Err          string
}
```

## Container creation (exact settings)

`Deploy` pulls the image if not present locally (pull policy: **if-not-present**, never `always` — local example builds must win), then creates:

| Setting | Value |
|---|---|
| Name | `osctf-chal-<slug>` |
| Labels | `osctf.managed=true`, `osctf.challenge_id=<uuid>`, `osctf.instance_id=<uuid>` |
| Network | user-defined bridge `osctf-challenges` (created on runtime init if absent) |
| Port publish | host `0.0.0.0:<HostPort>` → container `<InternalPort>/tcp` |
| Memory | `HostConfig.Memory = MemLimitMB MiB`, `MemorySwap` = same value (no swap) |
| CPU | `HostConfig.NanoCPUs = CPUMillis * 1e6` |
| Pids | `HostConfig.PidsLimit = 256` |
| Privileges | `SecurityOpt: ["no-new-privileges:true"]`, `CapDrop: ["ALL"]`, `CapAdd: []` |
| Filesystem | root FS left writable in v0.1 (many challenge images need /tmp); revisit read-only + tmpfs in v0.2 hardening |
| Restart policy | `on-failure` with max 3 retries — a crashing challenge should not flap forever |
| Env | `spec.Env` merged over `{"FLAG": <challenge flag>}` — the platform injects the flag so images stay flag-free (see 13) |
| Seccomp/AppArmor | Docker defaults (do not disable) |

**Why flags are injected**: challenge images must not bake flags in; organizers can then change a flag without rebuilding. `FLAG` is the contract env var; `challenge.yaml` may opt out with `inject_flag: false` for challenges where the flag lives elsewhere by design.

## State machine

```
pending ──create/start ok──▶ running ──health fail──▶ unhealthy ──health ok──▶ running
   │                            │                          │
   └──any failure──▶ error      ├──Stop()──▶ stopped ──Deploy()/restart──▶ starting ─▶ running
                                └──container gone (reconcile)──▶ lost
```

- `Deploy` on `stopped`/`error`/`lost` is the retry path: destroy remnants (by label), create fresh. Idempotent on `running` (returns current instance).
- **Health**: v0.1 health = "container is running" (Docker inspect `State.Running`), plus a TCP dial to `127.0.0.1:<HostPort>` with 3 s timeout. Both pass → `running`, update `last_health_at`; dial fails while container runs → `unhealthy`. Performed by `Status()` and the reconcile ticker. (Container-defined HEALTHCHECKs are honored when present: `State.Health.Status == "healthy"` substitutes for the TCP dial.)
- `Logs`: `ContainerLogs` with `Tail: <n>`, stdout+stderr demuxed, ANSI stripped, capped at 256 KB.

## Reconcile (boot + every 60 s)

The DB is the desired state; Docker is actual state. `Reconcile`:

1. List containers with label `osctf.managed=true` (including stopped).
2. For each DB instance row: no matching container → `state=lost` (admin redeploys manually; do not auto-heal in v0.1 — surprise restarts during an event are worse than a visible `lost` badge). Container exists → refresh state/health as above.
3. For each labeled container with **no** DB row (orphan — e.g. DB was reset): stop and remove it, log a warning. The label namespace makes this safe — the runtime never touches unlabeled containers.

All reconcile writes go through the instances store; the ticker uses a 30 s context timeout.

## Port allocation

At deploy request time (before calling the runtime): `SELECT` used ports from `instances`, pick the lowest free port in `[OSCTF_PORT_RANGE_START, OSCTF_PORT_RANGE_END]` (default 30000–30999), insert the row with it — `uq_instances_host_port` arbitrates concurrent races (retry once with the next port on unique-violation). Range exhausted → 409 with a clear detail. The port is released only by `Destroy` (row's `host_port` set NULL on `stopped`? No — **stopped keeps its port**, so restart preserves connection info; only Destroy frees it).

## Connection info

`connection_template` on the challenge (e.g. `nc {host} {port}`, `http://{host}:{port}`) is rendered with `{host}` = `OSCTF_PUBLIC_HOST` (default: hostname of `OSCTF_BASE_URL`) and `{port}` = instance host port. Rendered server-side; participants get the final string only while `state == running`. Template absent → default `nc {host} {port}` for tcp, since we can't know better.

## Failure handling rules

- Image pull failure, create failure, start failure → row `state=error`, `error=<one-line cause>`; the admin UI shows it verbatim. Never leave a row in `pending`/`starting` on a failed path (wrap deploy in a defer that errors the row on panic/timeout).
- Deploy timeout: 120 s overall context; on timeout, best-effort remove the half-created container by label, then `error`.
- Docker daemon unreachable: `readyz` stays 200 (platform works without the runtime); challenge deploy returns 503-mapped error `runtime unavailable`; reconcile logs and skips. The scoreboard must never depend on Docker being up.

## Event-end cleanup

None automatic in v0.1 (organizers often keep challenges up after ends_at for practice). The event-running guide documents `docker ps --filter label=osctf.managed=true` and the admin UI Destroy buttons. Automatic TTL cleanup arrives with the v0.2 scheduler.

## Security notes (v0.1 posture, stated honestly in docs)

- The API container mounts `/var/run/docker.sock` — this is **root-equivalent on the host**. The install guide must say so plainly and recommend a dedicated VM for hosting events. Mitigation beyond that (socket proxy, rootless) is deliberately deferred; do not silently add complexity.
- Shared instances mean participants share state inside a challenge container — challenge authors are warned in the authoring guide (13) to design accordingly (no destructive writes, or periodic self-reset in the image).
- Kernel-hostile challenges (pwn with kernel exploits) are out of scope for shared Docker isolation; the authoring guide says so (vision doc's isolation-depth open question lands in v0.2 hardening).
- Container egress: leave enabled in v0.1 (many web challenges fetch things); note in authoring guide. Revisit `--internal` network options in v0.2.

## Decision log (M7/M10)

- **Health check drops the `127.0.0.1:<HostPort>` TCP dial** (2026-07-25): doc's
  health definition assumed the platform runs on the host. In the golden-path
  compose deployment the platform runs in its own container and cannot reach the
  host-published port on `127.0.0.1`, which made every running instance report
  `unhealthy`. Health is now: container `State.Running` (plus the container's own
  `HEALTHCHECK` when defined). Participant reachability is validated by players
  connecting; a network-level probe from the platform container would require
  attaching it to the `osctf-challenges` bridge (revisit in v0.2 hardening).
- **Example container images are built on the host and not pushed**: the seeder
  creates the challenge rows on first boot; `make examples` builds
  `osctf/example-<slug>:0.1` locally, and the platform (sharing the host Docker
  socket) finds them via the if-not-present pull policy at deploy time.
