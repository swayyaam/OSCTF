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

- `name` matches `^[a-z0-9]+(-[a-z0-9]+)*$` and is unique across loaded plugins (a
  collision skips the later one with a warning).
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
- **Total wall-clock an operator experiences, first crash → quarantine:** the ~3 s of
  cumulative backoff **plus** the time to detect each of the 5 failures. A launch/handshake
  crash is detected in milliseconds, so a launch-crash loop quarantines in **~3–5 s**; a
  plugin that serves for `T` seconds before crashing each time takes ~`5·T` longer (it runs,
  then dies, five times). Either way it reaches a stable `failed` in bounded time — seconds
  for a fast crash, not minutes and never forever.
- **On quarantine**, the registry slot falls back to the built-in default where one exists
  (scoring/challenge-type) or returns a clear error on use (auth, which has no fallback).
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
plugin child can be orphaned. Two mechanisms, named:

- **Primary — go-plugin parent-death detection.** The plugin's `plugin.Serve` watches the
  RPC connection to the host and exits when it closes; a `SIGKILL`ed host drops that
  connection, so a well-behaved plugin self-terminates. This is the common case and needs
  no extra machinery.
- **Backstop — pidfile + boot-time orphan sweep.** Each launch writes a per-plugin
  **pidfile** (`$OSCTF_RUNTIME_DIR/plugins/<name>.pid`) recording the child PID and a start
  token. On the next `serve` boot, before relaunching, the loader reads any stale pidfiles
  and kills a surviving process whose start token doesn't match a live child — a **boot
  reconciliation** that reclaims what a dead core left behind. This is the direct analogue
  of the stale-instance reaper that fixed the port leak: don't trust graceful teardown to
  have run; sweep orphaned resources on boot. (Plugins are also launched in their own
  process group so a supervisor — systemd/compose — can kill the group as a third line.)

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
| **Reload is idempotent.** Reloading a healthy plugin twice yields one live process and one registry entry — no duplicate registration, no leaked old process. | Reload ×2; assert one PID + one registry entry + no goroutine/socket growth. |
| **The core never blocks indefinitely on a hung plugin call.** Every host→plugin call is deadline-bounded; a plugin that never returns yields `DEADLINE_EXCEEDED` within the per-call timeout and the host stays responsive. | A hung-plugin double; assert the call returns within the deadline and other requests are unaffected. |
| **No goroutine, socket, or child outlives a `stopped` plugin.** The per-plugin context cancels all of them on stop. | `goleak` + an fd/PID count around a load→serve→stop cycle (the residue guard, extended to plugins). |
| **A challenge-type plugin outage never consumes a player's attempt.** A failed `CheckFlag` rolls back with no solve *and* no `max_attempts` decrement. | Fail a `regex-flag` plugin mid-`CheckFlag`; assert the submission errors, no solve is recorded, **and** the team's attempt count is unchanged. |
| **The served scoreboard is always recomputable from the submission log** — whether the scoring fallback is off (default) or on. If it fired, the submission carries a `scored_by=fallback` marker so recompute is exact. | With the fallback off and on, force a scoring-plugin failure, then recompute the board from the log and assert it equals the served board (the v0.2 soak invariant, extended to the plugin path). |
| **A dropped notification is always observable** — never silent. | Fail/hang a notification subscriber; assert the event is dropped, the originating action still commits, **and** `osctf_plugin_events_dropped_total{name,event}` + a log line record it. |
| **A plugin cannot push the core past its resource budget.** Total plugin processes ≈ plugins loaded; concurrent in-flight host→plugin calls never exceed `OSCTF_PLUGIN_MAX_INFLIGHT` (over-cap sheds `503`). | Load N plugins, drive more concurrent calls than the cap; assert in-flight is bounded by the semaphore, excess sheds `503`, process count stays ≈ N, and the host stays responsive (the argon2-gate lesson). |

## Trust model (documented limitation)

Plugins are **trusted but isolated**. They run as separate processes under the platform's
user, can make network calls, and read the config handed to them — but cannot crash the
host or share its memory. v0.3 does **not** add a syscall sandbox (seccomp/namespaces)
around plugins; operators should only install plugins they trust, exactly as with any
server extension. Stronger sandboxing is a future hardening item, not a v0.3 deliverable.
This is stated plainly in the operator docs.

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
- **No live directory watching in v0.3.** Rescan on restart or explicit reload; a
  filesystem watcher is complexity without a strong v0.3 need.
