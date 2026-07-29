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
   interval; go-plugin also detects a dead child. State (`running`/`unhealthy`/`stopped`/
   `errored` + last error) is exposed at `GET /api/v1/admin/plugins`.
4. **Restart.** A plugin that exits or goes unhealthy is restarted with capped exponential
   backoff (a few attempts); after the cap it stays `errored` and its registry slot falls
   back to the built-in default (where one exists) or returns a clear error on use.
5. **Reload.** `POST /api/v1/admin/plugins/{name}/reload` (admin) re-reads the manifest and
   relaunches the one plugin. A full rescan happens on `serve` restart; hot add/remove of
   plugin *directories* is a rescan, not watched live in v0.3.
6. **Shutdown.** On `serve` shutdown the loader signals every plugin to exit (go-plugin
   `Kill`) and reaps children within the existing 10 s drain window.

## Failure isolation (the non-negotiable)

Every host→plugin call is wrapped:

- **Timeout-bounded** — a per-call context deadline (auth/challenge-type ~5 s; notify
  ~2 s, async). A hung plugin yields `DEADLINE_EXCEEDED` → 504, never a stuck request.
- **Panic/crash-contained** — the plugin is a separate process; its death is a gRPC
  `UNAVAILABLE` → 502 (or a fallback), and the supervisor restarts it. The host never
  shares an address space with plugin code.
- **Fallback where safe** — for scoring and challenge-type checks a built-in default
  exists; on plugin failure the host can fall back (logged) so scoring/submission still
  works. For auth there is no silent fallback (a broken OIDC plugin must not silently log
  people in via something else) — it errors clearly and the other providers still work.
- **Observed** — every plugin failure increments `osctf_plugin_errors_total{name,type}`
  and logs with the plugin name; nothing fails silently.

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
| `OSCTF_PLUGIN_<NAME>_<KEY>` | — | Per-plugin config/secret override (upper-snake of the manifest key). |

## Decision log

- **Directory + manifest discovery, not a config-file registry.** Dropping a plugin
  directory in and restarting is the simplest thing an operator (or an agent running
  `osctf plugin install`) can do; the manifest is self-describing.
- **Built-ins are protected from override by default.** A plugin can't silently replace
  `email` auth or `static` scoring without an explicit `override: true` — avoids a
  malicious/buggy plugin hijacking core behaviour.
- **Restart with backoff, then fall back — don't thrash.** Bounds the blast radius of a
  crash-looping plugin.
- **No live directory watching in v0.3.** Rescan on restart or explicit reload; a
  filesystem watcher is complexity without a strong v0.3 need.
