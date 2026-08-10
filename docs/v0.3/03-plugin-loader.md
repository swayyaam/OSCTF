# 03 — Plugin Loader & Lifecycle

The loader is the host side of the plugin system: it discovers plugins, validates their
manifests, launches and supervises the processes, wires them into the registries/bus, and
tears them down cleanly. Package: `api/internal/plugin`.

## Discovery & layout

`OSCTF_PLUGINS_DIR` (default `./plugins`) contains one directory per plugin:

```
plugins/
  osctf-oidc/
    plugin.yaml          # the manifest
    osctf-oidc           # the executable (or osctf-oidc.wasm-free binary)
  acme-scoring/
    plugin.yaml
    acme-scoring
```

The loader scans one level deep, reads each `plugin.yaml`, and ignores directories without
one (with a debug log). Discovery is deterministic (sorted by directory name) so load order
and any name-collision reporting are stable.

## Manifest — `plugin.yaml`

```yaml
name: oidc                       # unique; the registry key (auth provider id, scoring mode, …)
type: auth                       # auth | scoring | notification | challenge_type
abi: "1.0"                       # ABI major.minor the plugin was built against
version: "0.3.0"                 # the plugin's own semver (display only)
executable: osctf-oidc           # path relative to the plugin dir
description: "Log in with any OpenID Connect provider."
# Declared config: names, types, whether required, and whether secret (env-only).
config:
  issuer:        { type: string, required: true }
  client_id:     { type: string, required: true }
  client_secret: { type: string, required: true, secret: true }
  scopes:        { type: string, default: "openid email profile" }
```

Manifest validation (fail the plugin, not the host):

- `name` matches `^[a-z0-9]+(-[a-z0-9]+)*$` and is unique across loaded plugins. A
  collision — two plugins claiming the same `name`, i.e. the same registry key / auth
  provider id — **fails both** with a loud error; neither registers. Identity in an auth
  path is not resolved by load order, and the host still starts (the other plugins load).
  (A plugin colliding with a *built-in* key is separate: protected unless `override: true`.)
- `type` is one of the four; `abi` major equals the host's; `executable` exists and is a
  regular file with the owner-exec bit.
- `config` keys and types are well-formed; `secret: true` keys must **not** carry a value
  in the manifest (they come from env).

## Config resolution & secrets

For plugin `oidc`, each config key resolves in order: `OSCTF_PLUGIN_OIDC_<KEY>` env →
`plugin.yaml` `config.<key>.default` → error if `required` and still unset. Secret keys
(`secret: true`) resolve **only** from env; a value in the manifest is a validation error.
The resolved config is handed to the plugin once at startup via a `Configure` step
(carried in the go-plugin launch env or an initial RPC — see lifecycle). Secrets are never
logged, never returned by the plugins API, and never placed in event payloads.

## Lifecycle

```
discover → validate manifest → launch (go-plugin) → handshake + Info → configure →
register/subscribe → [serve + health] → drain → stop
```

1. **Launch.** The loader starts the executable via go-plugin with the ABI handshake and a
   per-plugin gRPC client. The plugin's config is provided at launch (env) and/or via a
   first `Configure(config)` RPC; the plugin validates it and fails fast on bad config.
2. **Info + register.** The loader calls `Info`, checks `name`/`type`/`abi` agree with the
   manifest, then registers the plugin into the matching registry (`auth`, `scoring`,
   challenge-type) or subscribes it to the event bus (notification, using `Subscriptions`).
   A registry key already owned by a built-in is **not** overridden unless the manifest
   opts in (`override: true`) — protecting `email`/`static`/`dynamic` by default.
3. **Health.** A background ticker calls each plugin's `Info` (cheap liveness) on an
   interval; go-plugin also detects a dead child. The state (`ready` / `unhealthy` /
   `restarting` / `failed` / `stopped` + last error — the state machine below) is exposed at
   `GET /api/v1/admin/plugins`.
4. **Restart.** A plugin that exits or goes `unhealthy` is restarted with capped exponential
   backoff; after the cap it is quarantined in `failed` and its registry slot falls back to
   the built-in default (where one exists) or returns a clear error on use. Full policy in
   *Concurrency & failure model → Restart policy & crash-loop quarantine*.
5. **Reload.** `POST /api/v1/admin/plugins/{name}/reload` (admin) re-reads the manifest and
   relaunches the one plugin. A full rescan happens on `serve` restart; hot add/remove of
   plugin *directories* is a rescan, not watched live in v0.3.
6. **Shutdown.** On `serve` shutdown the loader signals every plugin to exit (go-plugin
   `Kill`) and reaps children within the existing 10 s drain window.

## Failure isolation (the non-negotiable)

Every host→plugin call is wrapped:

- **Timeout-bounded** — a per-call context deadline (`OSCTF_PLUGIN_CALL_TIMEOUT`, default
  ~10 s for auth/challenge-type to allow a network round-trip like an OIDC token exchange;
  ~2 s for notify, async). A hung plugin yields `DEADLINE_EXCEEDED` → 504, never a stuck
  request.
- **Panic/crash-contained** — the plugin is a separate process; its death is a gRPC
  `UNAVAILABLE` → 502 (or a fallback), and the supervisor restarts it. The host never
  shares an address space with plugin code.
- **Fallback where safe** — for scoring and challenge-type checks a built-in default
  exists; on plugin failure the host can fall back (logged) so scoring/submission still
  works. For auth there is no silent fallback (a broken OIDC plugin must not silently log
  people in via something else) — it errors clearly and the other providers still work.
- **Observed** — every plugin failure increments `osctf_plugin_errors_total{name,type}`
  and logs with the plugin name; nothing fails silently.

## Concurrency & failure model (specified before implementation)

Process supervision, restart, health checks, and hot-reload are a **new concurrency
surface**, and the last two releases found production bugs of exactly this shape: a host
port leaked because a resource's owner died and nothing reclaimed it; a sweep was callable
in the wrong phase; a check-then-write ran without a lock. So the loader's state machine,
in-flight semantics, restart policy, resource ownership, and invariants are pinned **here,
before the loader exists** — and the invariant list at the end is written in the style of
the AGENTS.md testing contract so the tests are known before the code.

### The plugin state machine

Every plugin the loader tracks is in exactly one state. **Only `ready` serves new
requests**; `draining` finishes in-flight calls but admits no new ones; every other state
routes calls to the per-type fallback-or-error below.

| State | Meaning | Serves? |
|---|---|---|
| `discovered` | Manifest found + validated; not yet launched. | No |
| `launching` | Process started; handshake + `Info` + `Configure` in progress. | No |
| `ready` | Launched, configured, registered, health passing. | **Yes** |
| `unhealthy` | Was `ready`; a health check or a call failed, process maybe still alive; supervisor deciding. | No (transient) |
| `restarting` | Being torn down + relaunched; backoff between attempts. | No |
| `failed` | Restart cap exhausted (crash-loop) → **quarantined**; not retried automatically. | No |
| `draining` | Reload/shutdown in progress; no new calls, in-flight allowed to finish. | In-flight only |
| `stopped` | Process exited, resources reclaimed, registry entry removed. Terminal. | No |

Legal transitions (anything not listed is a bug the tests reject):

| From | To | Trigger |
|---|---|---|
| — | `discovered` | manifest found + validated on scan/reload |
| `discovered` | `launching` | loader starts the process |
| `launching` | `ready` | handshake + `Info` + `Configure` succeed → registered |
| `launching` | `restarting` | launch/handshake/configure failed, attempts remain |
| `launching` | `failed` | launch failed with the restart cap already reached |
| `ready` | `unhealthy` | health check fails, or a call returns `UNAVAILABLE`/`DEADLINE_EXCEEDED` |
| `unhealthy` | `ready` | a later health check passes (transient blip recovered) |
| `unhealthy` | `restarting` | process confirmed dead, or unhealthy past the threshold |
| `restarting` | `launching` | backoff elapsed → next attempt |
| `restarting` | `failed` | restart cap reached (crash-loop) → quarantine |
| `ready` / `unhealthy` | `draining` | reload or `serve` shutdown requested |
| `draining` | `stopped` | in-flight drained, or drain timeout → process killed, resources reclaimed |
| `failed` | `launching` | **explicit** operator reload (or `serve` restart) — the only exit from quarantine |

State is exposed at `GET /api/v1/admin/plugins` and as an `osctf_plugin_state{name,state}`
gauge, so the current state and the last transition are always observable.

**The crash-detection gap.** A process dies asynchronously; the crash-watcher's `procExited`
event, which drives the `ready → unhealthy/restarting` transition and the deregistration,
arrives *after* the process is already gone. In that window a `dispatch` can still see
`ready` and route a call to the dead client. This is safe **by isolation, not by luck**: the
call hits a broken gRPC connection and returns **`UNAVAILABLE`** (mapped to `502`), and the
per-call deadline is a second bound — never a host panic, never a hang. The caller gets a
clean mapped error and re-initiates; the supervisor then deregisters. Pinned directly with
the `crashafter` double (call *during* the gap → assert a mapped error and a host still up).

### In-flight calls when a plugin dies or reloads — per type

The acceptable answer differs by plugin type, because their correctness requirements differ.
A call already dispatched when the plugin dies (or is swapped by a reload) resolves as:

| Type | On plugin death mid-call | Retry? | Fallback? | Why |
|---|---|---|---|---|
| **auth** | The login **errors** (`502`/clear message); the user re-initiates. | No | **No** | You must never complete a login by silently routing it elsewhere. A half-finished auth is a failed auth. |
| **scoring** | The submission **errors** (clear message); the player retries in ~10 s. | No | **Off by default** — opt-in static fallback (below) | Falling back to `static` mid-event means the board is computed under two rules depending on when each solve landed, and recovery is ambiguous (recompute → rankings shift retroactively; don't → permanently inconsistent). A retry costs a player nothing and corrupts nothing. |
| **challenge-type** (`CheckFlag`) | The submission **errors**, the tx **rolls back**, and **the attempt is not counted** against `max_attempts`. | No | **No** | Both fallbacks are catastrophic: accept-anything hands out free solves; reject-everything is indistinguishable from a wrong flag, so players **burn attempts against a broken plugin**. A plugin outage must not cost a player their tries. |
| **notification** | The event is **dropped**, **counted, and logged** — never silent. | No | n/a | The bus is best-effort and never gates an action (see the event-bus contract), but a drop is always observable. |

**Auth, challenge-type, and scoring all fail closed by default** — the caller gets a clear
error and retries, and nothing is corrupted. **Notification fails open** (drops, but counts +
logs). No type transparently retries a *single* in-flight call against a restarted process —
the caller re-initiates — because a transparent mid-call retry would double-execute side
effects.

**The one opt-in: scoring static fallback.** An operator who prefers availability over
consistency may set `OSCTF_PLUGIN_SCORING_FALLBACK=true`; then a scoring-plugin failure falls
back to `static` for that computation instead of erroring. When it fires, the core **records
on the submission that it was scored by fallback** (a `scored_by=fallback` marker), so the
scoreboard remains **exactly recomputable from the submission log** afterward — the
board-recomputability invariant below (and the v0.2 soak invariant) must hold whether or not
the fallback is enabled. A *silent* fallback that left no marker would break it, which is
why the marker is mandatory when the opt-in is on.

### Hot-reload drains, it does not cancel

Reload (`POST …/plugins/{name}/reload` or a manifest change on rescan) launches the **new**
instance in parallel and swaps the registry entry **only once the new instance reaches
`ready`** — so there is no capability gap during reload. The **old** instance goes
`draining`: it admits no new calls, and its in-flight calls are given up to
`OSCTF_PLUGIN_DRAIN_TIMEOUT` (default **30 s**) to complete, then the process is killed and
reclaimed. 30 s, not 6 — a plugin doing network I/O (an OIDC token round-trip) can legitimately
take longer than a few seconds, and cutting it off would fail a call that was about to
succeed. The drain timeout must be `≥` the per-call timeout so an in-flight call can finish
within it; both are configurable (`OSCTF_PLUGIN_DRAIN_TIMEOUT`, and the per-call
`OSCTF_PLUGIN_CALL_TIMEOUT` — default 10 s for auth/challenge to allow the network round-trip,
2 s for notify). Graceful `serve` shutdown honors the same drain timeout (its shutdown budget
is raised to cover it), so a shutdown can take up to the drain window if a plugin call is
genuinely in flight.

**At the drain deadline** a still-in-flight call is **cancelled with a clear error** — its
context is cancelled, the caller receives `DEADLINE_EXCEEDED` (or `UNAVAILABLE` once the
process is gone), never a silent hang or a wrong result — and then the process is **killed**.
So in-flight work is *drained* up to the timeout and *cancelled-then-killed* past it, never
left dangling. Reload is **idempotent** — reloading a healthy plugin twice leaves one live
process and one registry entry, never a duplicate registration or a leaked old process.

### Restart policy & crash-loop quarantine

- **Backoff curve:** the delay before each retry is exponential (base 200 ms, ×2) with full
  jitter, capped at a 30 s ceiling. With the default cap of 5 attempts:

  | Before attempt | 2 | 3 | 4 | 5 | → quarantine |
  |---|---|---|---|---|---|
  | Delay (pre-jitter) | 200 ms | 400 ms | 800 ms | 1.6 s | — |
  | Cumulative backoff | 0.2 s | 0.6 s | 1.4 s | **3.0 s** | at attempt 5's failure |

- **Cap:** after **5** consecutive failed attempts the plugin goes `failed` and **stops
  retrying** — quarantined, never retried forever, never thrashing. Configurable via
  `OSCTF_PLUGIN_RESTART_CAP`; the 30 s ceiling only binds if the cap is raised past ~8.
- **The counter resets only after *sustained* health, not on merely reaching `ready`.** A
  plugin that reaches `ready`, then crashes 10 s later, then relaunches to `ready`, then
  crashes again — forever — would *never* quarantine if the counter reset on every `ready`,
  yet it is exactly the slow crash-loop nobody notices because the plugin *appears* to work.
  So the consecutive-failure counter is reset **only after the plugin has been continuously
  `ready` for `OSCTF_PLUGIN_HEALTH_STABLE` (default 60 s)** — a stability timer armed on
  entering `ready` and cancelled by any exit before it fires. A plugin that crashes on a
  fixed interval shorter than that window accumulates failures across restarts and reaches
  `failed` in bounded time; one stable for ≥ 60 s is treated as recovered. Pinned by the
  crash-interval invariant below.
- **Total wall-clock an operator experiences, first crash → quarantine:** the ~3 s of
  cumulative backoff **plus** the time to detect each of the 5 failures. A launch/handshake
  crash is detected in milliseconds, so a launch-crash loop quarantines in **~3–5 s**; a
  plugin that serves for `T` seconds before crashing each time takes ~`5·T` longer (it runs,
  then dies, five times). Either way it reaches a stable `failed` in bounded time — seconds
  for a fast crash, not minutes and never forever.
- **On quarantine (and on any exit from `ready`), the registry entry is handled per type —
  fail-closed by default, never a silent revert to a built-in scorer/checker**, because
  scoring and challenge-type were decided fail-closed (silently re-scoring under `static` or
  re-checking with a built-in is the behaviour we rejected):

  | Type | Registry action on leaving `ready` | A new call then… |
  |---|---|---|
  | **auth** | entry **removed** | that provider errors clearly; other providers (incl. built-in `email`) unaffected |
  | **scoring** | entry **removed** (fail closed) — *unless* `OSCTF_PLUGIN_SCORING_FALLBACK=true`, then reverted to `static` **with a `scored_by=fallback` marker** | submission errors (default), or is scored `static` and marked (opt-in) |
  | **challenge-type** | entry **removed** (fail closed) | `CheckFlag` errors, tx rolls back, **attempt not consumed** — never accept-anything, never reject-anything |
  | **notification** | subscription **removed** | event dropped, **counted + logged** (fails open, but observable) |

  Only scoring's explicit opt-in reverts to a built-in; every other case removes the entry
  so the call fails closed. "Revert to built-in" is **not** the default for any type.
- **Operator visibility:** `GET /api/v1/admin/plugins` shows `state=failed` with the
  restart count, last error, and last-attempt time; `osctf_plugin_errors_total` and the
  `osctf_plugin_state` gauge reflect it; a loud `WARN` names the plugin. The **only** way
  out of `failed` is an explicit reload or a `serve` restart — a quarantine never clears
  itself, so a broken plugin can't silently start working (or looping) again unnoticed.

### Resource ownership & orphan reclamation (the port-leak shape)

This is the exact shape of the v0.2 port leak — *a resource whose owner died and nothing
swept it* — so ownership is explicit. **When a plugin leaves `draining`/`failed` for
`stopped`, the core reclaims, in order:**

1. **the in-flight call contexts** — cancelled (their deadlines fire; callers get
   `UNAVAILABLE`/`DEADLINE_EXCEEDED`), so no goroutine waits on a dead process;
2. **the registry entry / bus subscription** — removed or reverted to the built-in
   **atomically before** the process is gone, so a lookup never resolves to a dead plugin;
3. **the goroutines** — the per-plugin health ticker, the gRPC client, and any call
   watchers are bound to a per-plugin `context` that is cancelled on stop (goleak-checked);
4. **the socket/pipe** — the go-plugin gRPC transport (unix socket / stdio pipe) is closed;
5. **the child process** — killed (`go-plugin.Kill`) and **reaped** (`wait`), so no zombie.

**If the core crashes first** (a hard `SIGKILL`, where graceful teardown never runs) a
plugin child can be orphaned. **go-plugin (v1.8.0) provides no parent-death of its own** —
verified in its source: no `prctl`/`Pdeathsig`, no process group, and the plugin's
`plugin.Serve` loop watches neither stdin nor the RPC connection, so a child does **not**
self-exit when the host dies. The host reaps the child on a graceful `Kill`; on a host
*crash* nothing in go-plugin reclaims it. So the loader sets it up itself:

- **Kernel parent-death (Linux) — `SysProcAttr.Pdeathsig = SIGKILL`.** The loader sets it on
  the `exec.Cmd` it hands go-plugin, so the kernel `SIGKILL`s the child the moment the host
  process dies. Reliable, needs no plugin cooperation — the **primary on the Linux deploy
  target.**
- **Process group — `SysProcAttr.Setpgid = true`** on every platform, so an external
  supervisor (systemd/compose) can kill the whole group as a second line.
- **Backstop — pidfile + boot-time orphan sweep.** Each launch writes a per-plugin
  **pidfile** (`$OSCTF_RUNTIME_DIR/plugins/<name>.pid`) recording `{pid, start-token}`, and
  the child is launched with that token in its argv. On the next `serve` boot, before
  relaunching, the loader reads any stale pidfile, checks the PID is alive, and kills it
  **only if that PID's current command line still carries our token** — positive
  identification, so a recycled PID (a different process) is never killed; on any ambiguity
  (dead PID, unreadable cmdline, no token) it refuses and logs. This is the stale-instance
  reaper's discipline: reconcile on boot, and only act on what you can positively identify.

- **Platform gap (stated, not assumed away):** `Pdeathsig` is Linux-only. On **macOS /
  Docker Desktop (the developer path)** there is no kernel parent-death, so a hard host
  crash leaves an orphaned child until the **next boot sweep** reclaims it — the sweep is
  **load-bearing there, not a rare backstop**. Noted here beside the no-sandbox isolation
  caveat so the dev-path window is a known limitation, not a surprise.

### Resource budget — a plugin can't push the core past its own limits

The v0.2.1 lesson was that an **unbounded per-request resource** (argon2's memory, at ~100
concurrent sign-ups) OOMs the host; **N plugins each holding in-flight calls is the same
shape**, so plugin resource use is bounded, not hoped:

- **Total plugin processes are bounded** by the loaded set — one long-lived process per
  plugin, launched once at boot (discovery is a fixed one-level scan), never per request.
  The restart cap means even a crash-looping plugin does not spawn without bound: at most one
  restart in flight per plugin, then quarantine. So process count ≈ (plugins loaded), full
  stop.
- **Total concurrent in-flight host→plugin calls are capped** by a global semaphore
  (`OSCTF_PLUGIN_MAX_INFLIGHT`, default **64**) — the direct analogue of the argon2 gate. A
  request that would exceed it waits briefly, then fails `503` (load-shed), so a slow or
  popular plugin cannot pin an unbounded number of host goroutines/buffers and drag down the
  whole server. Auth's own admission limits still apply upstream.
- **Per-plugin memory is an operator expectation, not an enforced limit in v0.3.** There is
  no cgroup/rlimit around a plugin (same boundary as the no-syscall-sandbox note): a plugin
  is trusted to be bounded, and the operator sizes the host for the plugins they install. The
  manifest may carry an advisory memory hint; hard enforcement is the future sandboxing item.
  Stated plainly so the budget is a conscious decision, not an accident.

### Invariants the tests pin (written now, AGENTS.md-style)

These are fixed before the loader is built; each is a named test in the `plugins`/`api-test`
suites ([`09-testing-ci.md`](09-testing-ci.md)). They are never weakened to make a build go
green.

| Invariant | Pinned by (test to write) |
|---|---|
| **No orphaned plugin process survives core shutdown or crash.** Graceful shutdown reaps every child; after a simulated hard `SIGKILL` of the host, the child self-exits (parent-death) or the next boot's pidfile sweep kills it. | Launch a plugin, `SIGKILL` the host harness, assert the child is gone (directly or after a boot sweep). |
| **A plugin in any non-`ready` state never serves a request.** Routing dispatches only to `ready`; every other state gets the per-type fallback-or-error, never a call to the process. | State-machine table test + a routing test that drives each state and asserts no call reaches a non-`ready` process. |
| **A registry never holds an entry for a stopped plugin.** Stop reverts/removes the entry atomically before the process dies; a lookup after stop yields the built-in or "not found". | Stop-then-lookup test; removing all plugins leaves a registry byte-identical to v0.2. |
| **A crash-looping plugin cannot exhaust process or fd limits.** The cap + quarantine bound total children/sockets; a launch-crash plugin reaches `failed` in ≤ 5 attempts and stops spawning. | A crash-on-launch double; assert ≤ 5 processes/sockets ever created and a terminal `failed`. |
| **A plugin that crashes on a fixed interval indefinitely eventually quarantines.** The consecutive-failure counter resets only after `OSCTF_PLUGIN_HEALTH_STABLE` of continuous `ready`, so a slow crash-loop (reach `ready`, crash before the stability window, repeat) still accumulates to `failed` — the "appears to work" crash-loop is not immortal. | A double that serves then crashes on a fixed interval shorter than the stability window (injected clock); assert it reaches `failed` after the cap rather than resetting forever. |
| **Reload is idempotent.** Reloading a healthy plugin twice yields one live process and one registry entry — no duplicate registration, no leaked old process. | Reload ×2; assert one PID + one registry entry + no goroutine/socket growth. |
| **The core never blocks indefinitely on a hung plugin call.** Every host→plugin call is deadline-bounded; a plugin that never returns yields `DEADLINE_EXCEEDED` within the per-call timeout and the host stays responsive. | A hung-plugin double; assert the call returns within the deadline and other requests are unaffected. |
| **A call in the crash-detection gap fails cleanly.** A call routed to a plugin that just died (before `procExited` deregisters it) returns a mapped `UNAVAILABLE`/`502`, never a host panic or a hang. | The `crashafter` double: call during the gap and assert the caller gets a mapped error and the host stays up. |
| **No goroutine, socket, or child outlives a `stopped` plugin.** The per-plugin context cancels all of them on stop. | `goleak` + an fd/PID count around a load→serve→stop cycle (the residue guard, extended to plugins). |
| **A challenge-type plugin outage never consumes a player's attempt.** A failed `CheckFlag` rolls back with no solve *and* no `max_attempts` decrement. | Fail a `regex-flag` plugin mid-`CheckFlag`; assert the submission errors, no solve is recorded, **and** the team's attempt count is unchanged. |
| **The served scoreboard is always recomputable from the submission log** — whether the scoring fallback is off (default) or on. If it fired, the submission carries a `scored_by=fallback` marker so recompute is exact. | With the fallback off and on, force a scoring-plugin failure, then recompute the board from the log and assert it equals the served board (the v0.2 soak invariant, extended to the plugin path). |
| **A dropped notification is always observable** — never silent. | Fail/hang a notification subscriber; assert the event is dropped, the originating action still commits, **and** `osctf_plugin_events_dropped_total{name,event}` + a log line record it. |
| **A plugin cannot push the core past its resource budget.** Total plugin processes ≈ plugins loaded; concurrent in-flight host→plugin calls never exceed `OSCTF_PLUGIN_MAX_INFLIGHT` (over-cap sheds `503`). | Load N plugins, drive more concurrent calls than the cap; assert in-flight is bounded by the semaphore, excess sheds `503`, process count stays ≈ N, and the host stays responsive (the argon2-gate lesson). |
| **A registry swap is atomic from a reader's perspective.** The registries (auth / scoring / challenge-type) hold an atomic pointer to an immutable map; register/reload builds a new map and swaps the pointer. No reader ever observes a partially updated registry, and a lookup in flight during a swap resolves to either the old value or the new one — never nothing, never a torn map. | Hammer `Get(name)` from many goroutines while another goroutine registers/reloads; under `-race`, assert every lookup returns a valid provider (old or new), never `nil`/absent for a name that exists in both maps, and no torn read. |

## Trust model (documented limitation)

Plugins are **trusted but isolated**. They run as separate processes under the platform's
user, can make network calls, and read the config handed to them — but cannot crash the
host or share its memory. v0.3 does **not** add a syscall sandbox (seccomp/namespaces)
around plugins; operators should only install plugins they trust, exactly as with any
server extension. Stronger sandboxing is a future hardening item, not a v0.3 deliverable.
This is stated plainly in the operator docs.

**The plugins directory should be read-only to the platform process.** Discovery only
reads `OSCTF_PLUGINS_DIR`; the core never writes there. Mounting it read-only to the
process removes a persistence path — a compromised core cannot drop a new plugin binary
+ manifest for the next boot to launch. v0.3 does not *enforce* this (the loader doesn't
check the mount), but the deployment doc states it as the expected posture, the same way
the Docker-socket mount is called out as root-equivalent. Installing a plugin is then an
explicit operator action (write to the dir out-of-band, then reload/restart), not
something core code can do to itself.

## Config (new env)

| Env | Default | Meaning |
|---|---|---|
| `OSCTF_PLUGINS_DIR` | `./plugins` | Directory scanned for plugin subdirectories. |
| `OSCTF_PLUGINS_ENABLED` | `true` | Master switch; `false` skips the loader entirely (pure-core mode). |
| `OSCTF_PLUGIN_CALL_TIMEOUT` | `10s` (notify `2s`) | Per-call deadline; a hung call yields `DEADLINE_EXCEEDED`. Longer default for network round-trips (OIDC). |
| `OSCTF_PLUGIN_DRAIN_TIMEOUT` | `30s` | Grace for a plugin's in-flight calls on reload/stop before the process is killed. Must be `≥` the call timeout; the `serve` shutdown budget is raised to cover it. |
| `OSCTF_PLUGIN_RESTART_CAP` | `5` | Consecutive failed restarts before quarantine (`failed`). |
| `OSCTF_PLUGIN_MAX_INFLIGHT` | `64` | Global cap on concurrent in-flight host→plugin calls (a semaphore); over-cap calls wait briefly then fail `503`. Bounds plugin-induced host load. |
| `OSCTF_PLUGIN_SCORING_FALLBACK` | `false` | Opt-in: on a scoring-plugin failure, fall back to `static` (recorded per-submission) instead of erroring the submission. Availability over consistency. |
| `OSCTF_RUNTIME_DIR` | (OS temp) | Where per-plugin pidfiles are written for the boot-time orphan sweep. |
| `OSCTF_PLUGIN_<NAME>_<KEY>` | — | Per-plugin config/secret override (upper-snake of the manifest key). |

## Decision log

- **Directory + manifest discovery, not a config-file registry.** Dropping a plugin
  directory in and restarting is the simplest thing an operator (or an agent running
  `osctf plugin install`) can do; the manifest is self-describing.
- **Built-ins are protected from override by default.** A plugin can't silently replace
  `email` auth or `static` scoring without an explicit `override: true` — avoids a
  malicious/buggy plugin hijacking core behaviour.
- **Restart with backoff, then quarantine — don't thrash or loop forever.** A crash-looping
  plugin reaches `failed` after the cap and stays there (registry falls back or errors)
  until an explicit reload; this bounds its process/fd consumption and makes the failure
  visible instead of silent.
- **Orphan reclamation is a boot sweep, not a hope.** go-plugin parent-death handles the
  graceful case; a per-plugin pidfile + a boot-time sweep reclaims a child a hard-crashed
  core left behind — the same "reconcile on boot, don't trust teardown" lesson as the v0.2
  port-leak reaper. Named explicitly because this is the exact bug shape that recurs.
- **Hot-reload swaps on `ready` and drains the old, bounded by a timeout.** No capability
  gap during reload, in-flight work finishes rather than being cancelled, and reload is
  idempotent.
- **The state machine and invariants are fixed before the loader exists.** This is a new
  concurrency surface and the last two releases found bugs of exactly this shape; the
  invariant table is written in the testing-contract style so the tests precede the code.
- **No live directory watching in v0.3 — discovery at boot + explicit reload.** Discovery
  is a one-level scan at `serve` boot; after that, a plugin appears or disappears from the
  registry only via `POST …/plugins/{name}/reload` or a `serve` restart. **Dropping a
  plugin directory in while the platform runs does nothing until a reload/restart** — stated
  in the operator doc so it isn't a surprise. Watching the filesystem is a *different*
  failure surface than an explicit trigger — a partial binary mid-copy, an editor temp file,
  a manifest visible before its binary, unreliable events on network filesystems — and would
  need a settle delay plus a rule for a manifest whose binary isn't there yet. That
  complexity buys little in v0.3, where a plugin install is already an explicit operator step.
- **A name collision fails both plugins, it does not pick one by order.** Two plugins
  claiming the same `name` (= registry key = auth provider id) both fail with a loud error;
  neither registers, and the host still starts. Resolving ambiguous identity — especially an
  auth provider id — by discovery sort order is exactly the kind of silent, order-dependent
  behaviour to avoid. (Overriding a *built-in* key is the separate, opt-in `override: true`
  path.)
- **The plugins directory is expected read-only to the process.** The core only reads it;
  making it read-only removes a persistence path for a compromised core. Not enforced in
  v0.3, stated as deployment posture (see Trust model).
