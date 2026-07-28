# 03 — Runtime (per-team instances + hardening)

Baseline: [`../v0.1/08-challenge-runtime.md`](../v0.1/08-challenge-runtime.md) and the code
in `api/internal/runtime/` (`runtime.go`, `manager.go`, `docker.go`, `fake.go`). v0.2
changes are **additive**: the `ChallengeRuntime` interface keeps every v0.1 method
signature; `InstanceSpec` grows fields; `Manager` grows owner-aware methods; `DockerRuntime`
grows per-team networks and hardening. `FakeRuntime` mirrors the spec fields but stays
container-free.

## `ChallengeRuntime` — unchanged signatures

The four-method-plus interface ([`runtime.go:58`](../../api/internal/runtime/runtime.go#L58))
is **not modified**. `Deploy(ctx, InstanceSpec)` still carries all desired state; we only
add fields to `InstanceSpec`. This is the whole point of the v0.1 design — a Kubernetes
runtime in v0.4 implements the same interface.

## `InstanceSpec` — new fields (additive)

```go
type InstanceSpec struct {
    // v0.1 fields (unchanged):
    InstanceID   uuid.UUID
    ChallengeID  uuid.UUID
    Slug         string
    Image        string
    InternalPort int
    HostPort     int
    MemLimitMB   int
    CPUMillis    int
    Env          map[string]string   // already carries FLAG

    // v0.2 additions:
    TeamID         *uuid.UUID // nil = shared instance (v0.1); set = per-team
    NetworkName    string     // docker network to attach; "" = shared 'osctf-challenges'
    Internal       bool       // true → per-team network created --internal (egress off)
    ReadonlyRootfs bool       // true → read-only container rootfs
    Tmpfs          []string   // writable tmpfs mount targets, e.g. ["/tmp","/run"]
}
```

`Env["FLAG"]` continues to carry the flag — the **Manager** decides its value
(per-instance flag when present, else `challenges.flag`); the runtime just injects it. The
runtime never knows about flag *mode*.

`Instance` (observed state) gains nothing structural; the Manager attaches `TeamID`,
`ExpiresAt`, `Network` from the row when it builds participant/admin payloads.

## `Manager` — owner-aware methods

The Manager stays the DB-backed lifecycle layer (port allocation, rows, spec build,
connection info). v0.1 methods are keyed by `challengeID` and assume one instance; v0.2
adds team-keyed and id-keyed methods. Suggested surface:

```go
// Shared path — v0.1 behaviour (team_id NULL). Rename of the existing Deploy/Get/Destroy.
func (m *Manager) DeployShared(ctx, challengeID) (Instance, error)          // == v0.1 Deploy
func (m *Manager) GetShared(ctx, challengeID)   (Instance, bool, error)     // == v0.1 Get

// Per-team path — v0.2. The scheduler calls these; it owns quota/flag/TTL decisions.
func (m *Manager) DeployForTeam(ctx, DeployReq) (Instance, error)
func (m *Manager) GetTeamInstance(ctx, challengeID, teamID) (Instance, bool, error)
func (m *Manager) DestroyInstance(ctx, instanceID uuid.UUID) error   // by id, frees port; team-safe
func (m *Manager) ListTeamInstances(ctx, teamID) ([]Instance, error)
func (m *Manager) CountTeamRunning(ctx, teamID) (int, error)

type DeployReq struct {
    ChallengeID uuid.UUID
    TeamID      uuid.UUID
    Flag        string      // per-instance flag, or challenge.flag for static
    ExpiresAt   *time.Time  // nil = no TTL
}
```

`DeployForTeam`:

1. `ensureTeamRow` — `GetTeamInstance`; if none, `allocate` a port and insert a row with
   `team_id`, `flag`, `expires_at`, `network = "osctf-team-"+short(teamID)`. The
   `allocate` retry loop now also arbitrates on `uq_instances_per_team` (a concurrent
   start for the same (challenge, team) returns the winner's row — idempotent Start).
2. Idempotent: a `running` row is returned unchanged.
3. Build `InstanceSpec` from the challenge row **and** the request: `Env["FLAG"]=req.Flag`,
   `TeamID=&req.TeamID`, `NetworkName=row.network`, `Internal=!ch.egress`,
   `ReadonlyRootfs=true`, `Tmpfs=append(["/tmp"], ch.writable_paths...)`.
4. `rt.Deploy(spec)` — synchronous, 120 s cap (unchanged).

Port allocation (`ListUsedPorts` → lowest free) is unchanged and already spans all rows,
so it naturally covers per-team instances. Only the widened range
(30000–32767, [`02-database.md`](02-database.md)) differs.

`ConnectionInfo` is unchanged (per-row already). `short(teamID)` = first 8 hex chars of the
UUID (network names must be short and DNS-safe); collisions across teams are impossible
within an event (UUID v7 prefix + full id in a label).

## `DockerRuntime` — per-team networks

New helper, generalizing `ensureNetwork`:

```go
func (d *DockerRuntime) ensureNamedNetwork(ctx, name string, internal bool) error
```

- Shared instances → `challengeNetwork` ("osctf-challenges"), `internal=false` (v0.1).
- Per-team instances → `spec.NetworkName` ("osctf-team-<short>"), `internal=spec.Internal`.
- `NetworkCreate(..., network.CreateOptions{Driver:"bridge", Internal: internal,
  Labels: {managedLabel:"true", "osctf.team": short}})`. The `osctf.managed` +
  `osctf.team` labels let reconcile find and GC them.

`Deploy` attaches the container to `spec.NetworkName` (falling back to `challengeNetwork`
when empty) instead of the hardcoded `challengeNetwork`. Everything else in `Deploy`
(pull-if-missing, port bindings, labels incl. `osctf.instance_id`, health) is unchanged.

**Isolation guarantee:** because each team's containers sit on their own bridge, and the
bridge is (optionally) `--internal`, a container on team A's network cannot reach a
container on team B's network — the success-criterion isolation test
([`09-testing-ci.md`](09-testing-ci.md)) asserts exactly this.

### Per-team network GC (in `Reconcile`)

After the existing orphan-container sweep, add: list networks with label
`osctf.managed=true` and name prefix `osctf-team-`; for each with **zero attached
containers**, `NetworkRemove`. This reclaims a team's bridge once its last instance is
destroyed (stop, expiry, or event-end cleanup). Networks in use are skipped; the sweep is
idempotent and connection-failure-tolerant (skip the pass, never error the ticker), like
the v0.1 reconcile.

## Hardening (the v0.1-deferred pass)

Applied to **every** instance the runtime deploys (shared and per-team) unless a field
opts out. In `Deploy`'s `HostConfig`:

| Control | v0.1 | v0.2 |
|---|---|---|
| `no-new-privileges` | ✅ present | unchanged |
| cap-drop ALL | ✅ present | unchanged |
| mem/swap/cpu/pids limits | ✅ present | unchanged |
| `RestartPolicy on-failure:3` | ✅ present | unchanged |
| **ReadonlyRootfs** | ❌ | `hostCfg.ReadonlyRootfs = spec.ReadonlyRootfs` (default true) |
| **tmpfs** | ❌ | `hostCfg.Tmpfs = {"/tmp":"rw,noexec,nosuid,size=64m", …writable_paths}` |
| **per-team network** | ❌ (single net) | `spec.NetworkName` |
| **egress off** | ❌ | per-team net `Internal: spec.Internal` |

`writable_paths` (from the challenge) each mount as a small tmpfs (`rw,nosuid`, capped;
default 64 MiB) so a read-only-rootfs challenge that needs a scratch dir (uploads, sqlite)
still works. A challenge needing a *large* or *persistent* writable area is a design smell
for a CTF task and is out of scope — document, don't build.

**Socket-mount caveat (restated from v0.1):** the platform still needs the Docker socket to
manage containers; that is the trust boundary. Hardening reduces blast radius *inside* a
challenge container; it does not sandbox the daemon. gVisor/rootless remain a future open
question ([`00-overview.md`](00-overview.md) scope-OUT).

## `FakeRuntime` — spec parity, no containers

`FakeRuntime.Deploy` reads the new `InstanceSpec` fields but ignores the container-only
ones (networks, tmpfs, rootfs); it moves the row to `running` exactly as in v0.1, now
carrying `team_id`/`flag`/`expires_at` through because those live on the row (written by
the Manager before `rt.Deploy`). This keeps every scheduler/handler test container-free.
Add a `Deployed []InstanceSpec` capture slice so tests can assert the hardening/network
fields the Manager set (e.g. "per_team challenge → spec.TeamID non-nil, Internal reflects
egress").

## Decision log

- **Extend `InstanceSpec`, not the interface.** Additive fields keep every runtime
  implementation source-compatible and honour the v0.1 "interface is the plugin surface"
  rule.
- **Manager builds the spec (incl. hardening).** One place decides read-only rootfs, tmpfs,
  network, and FLAG value, so the two runtimes (docker/fake) and the scheduler never
  disagree on isolation defaults.
- **Per-team network GC in reconcile, not on destroy.** Destroy already races with health
  refresh; deferring net cleanup to the idempotent 60 s reconcile avoids "remove a network
  another concurrent start just attached to."
- **`short(teamID)` = 8 hex chars.** Docker network names must be short/DNS-safe; the full
  team id stays in a label for exact matching.
