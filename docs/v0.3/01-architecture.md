# 01 — Architecture (v0.3 delta)

Same shape as before: one Go server process (`platform serve`), a modular monolith with
enforced package boundaries. v0.3 adds a **plugin host** (loader + ABI + registries + event
bus), a **client CLI**, and an **MCP server** — all around the existing core, none of it
in the request hot path unless a plugin is actually configured.

## The central idea: registries the loader extends

Today the core wires **one** implementation of each extensible interface directly into the
composition root ([`main.go`](../../api/cmd/platform/main.go)):

```go
provider := auth.NewEmailPasswordProvider(q, …)   // the only AuthProvider
scoring.Registry()                                // {"static": …, "dynamic": …}
runtime.NewManager(dockerRT, …)                   // the only ChallengeRuntime
storage.NewS3Store(…)                             // the only ObjectStore
```

v0.3 replaces the single-value wiring for the **extensible** interfaces with **registries**
that the plugin loader can add to at boot:

- `auth.Registry` — provider id → `AuthProvider` (built-in `email` + any auth plugins).
- `scoring.Registry` — already a map; plugins register additional modes.
- `challenges` challenge-type registry — challenge `kind`/`type` → `ChallengeType` (new).
- `events.Bus` — notification plugins subscribe to domain events (new).

The built-in implementations register themselves first and remain the defaults, so a
no-plugin deployment is byte-for-byte v0.2. `runtime` and `storage` keep their single-value
wiring in v0.3 (out of scope; see [`00-overview.md`](00-overview.md)).

## Why out-of-process gRPC (the plugin-mechanism decision)

The exit criterion — *a third party ships a working plugin without a PR against core* —
plus "plugin failures must not take the platform down" drive the choice. Options weighed:

| Mechanism | Isolation | Any language | No core recompile | Fit for our interfaces | Verdict |
|---|---|---|---|---|---|
| stdlib `plugin` (.so) | none (in-proc) | Go only | yes-ish | fragile: exact toolchain match, Linux-only | ✗ rejected |
| In-process registration (import core, build your own `main`) | none | Go only | recompile the binary | perfect type fit | ✗ (no isolation; forces a Go rebuild per plugin) |
| WASM (wazero/extism) | strong sandbox | many | yes | poor: host imports for streams/SDKs are heavy; ecosystem immature | ✗ deferred (post-1.0) |
| **HashiCorp go-plugin (gRPC subprocess)** | **process-level** | **any gRPC lang** | **yes** | **good for request/response types (auth, scoring, notify, challenge-type)** | ✅ **chosen** |

**Decision: HashiCorp go-plugin over gRPC.** Each plugin is a standalone executable that
serves a gRPC service; the host launches it as a child process, negotiates a handshake,
and calls it over a local socket. This is the exact model Terraform/Vault/Nomad use for
their provider/plugin ecosystems. It gives process isolation (a panicking plugin dies
alone), language independence (the ABI is protobuf), and hot-swap without recompiling core.
The four v0.3 plugin types are all request/response- or fire-and-forget-shaped, which maps
cleanly onto gRPC. The runtime and object-store interfaces (streaming `io.ReadCloser`,
Docker SDK handles) are the awkward ones over a process boundary — which is exactly why
they stay in-tree in v0.3.

Full ABI in [`02-plugin-abi.md`](02-plugin-abi.md); loader in
[`03-plugin-loader.md`](03-plugin-loader.md).

## Package layout — what changes

| Package | Change in v0.3 |
|---|---|
| `plugin` | **NEW.** The host: `Loader` (discover/launch/supervise), the go-plugin handshake, gRPC client stubs per type, health + restart. Imports `hashicorp/go-plugin`, `google.golang.org/grpc`, the generated `pluginpb`. |
| `plugin/proto` (`pluginpb`) | **NEW.** The `.proto` ABI and generated Go stubs (checked in, drift-gated like `apigen`). |
| `plugin/sdk` | **NEW.** Author-facing helpers a plugin binary imports to serve a plugin type with a few lines of `main`. Shipped as an importable package + the template repo ([`11-plugin-template.md`](11-plugin-template.md)). |
| `auth` | `AuthProvider` grows a redirect/OAuth-capable variant; add `auth.Registry` and the core `/auth/{provider}/*` routes that drive any provider. Built-in `email` registers itself. |
| `scoring` | `Registry()` becomes a mutable registry the loader adds to. `Value()` resolves through it. Engines unchanged. |
| `challenges` | New `ChallengeType` concept (id, config schema, optional custom flag check) + a registry; the built-in `standard`/`container` behaviour is the default type. |
| `events` | **Grows an event bus** (`events.Bus`): typed domain events published by the services, delivered to subscribers (notification plugins) asynchronously. Distinct from the existing event *window* service. |
| `tokens` | **NEW.** API tokens: issue/verify/revoke, scopes, hashing. Bearer-auth middleware alongside sessions. |
| `httpserver` | Mount the generated server under **both** `/api/v1` (canonical) and `/api/v0` (deprecated alias); Bearer-token middleware; deprecation headers on v0. |
| `handlers` | New endpoints: plugin listing/health, token management, and any auth-provider routes. No business logic moves. |
| `apigen` / `openapi` | `openapi.yaml` version bumped to `1.0.0`; servers list `/api/v1` (+ `/api/v0` deprecated). New paths for tokens + plugins. Regenerated. |
| `cmd/osctf` | **NEW.** The client CLI binary. |
| `cmd/platform` | `serve` starts the plugin loader + event bus; no new subcommands (CLI is separate). |

**Hard rules:**

1. The core **never imports plugin code**. It talks to plugins only through the generated
   gRPC client. A plugin binary imports `plugin/sdk` + `plugin/proto`, never core internals.
2. The CLI and MCP server **never import service packages or touch the DB**. They are
   API-v1 clients (the generated `openapi-fetch`-equivalent Go client), authenticated by
   API token.
3. A plugin call is always **failure-isolated and timeout-bounded**: a plugin that errors,
   panics, or hangs yields a domain error (mapped to 502/504) or a fallback to the built-in
   default; it never blocks a request indefinitely or crashes the host.
4. Registries have exactly one built-in default per interface, registered before any
   plugin, so removing every plugin restores v0.2 behaviour deterministically.

## Key flows (normative)

### Boot with plugins

1. `serve` builds config, DB, Redis, storage, runtime (as v0.2), and the **event bus**.
2. Built-ins self-register: `auth.Registry["email"]`, `scoring` static/dynamic, the
   `standard`/`container` challenge type.
3. The **plugin Loader** scans `OSCTF_PLUGINS_DIR`, reads each `plugin.yaml`, checks the
   ABI version, launches the executable via go-plugin, handshakes, and — per the manifest
   `type` — registers the plugin into the matching registry or subscribes it to the bus.
4. A plugin that fails to launch or mismatches the ABI is **logged and skipped**; the host
   continues. Health of every plugin is exposed at `GET /api/v1/admin/plugins`.
5. On shutdown, the loader signals every plugin to exit and reaps the child processes.

### A plugin-backed request (e.g. OIDC login)

1. `GET /api/v1/auth/oidc/login` → core looks up `auth.Registry["oidc"]` (a plugin) →
   calls `BeginAuth` over gRPC → gets a redirect URL → 302s the browser to the IdP.
2. IdP redirects back to `GET /api/v1/auth/oidc/callback?...` → core calls
   `CompleteAuth(params)` over gRPC → plugin returns an external identity → core maps it to
   (or provisions) a local user and issues a session, exactly like email login.
3. If the plugin is down: 502 with a clear problem+json; the built-in `email` login still
   works.

### A domain event → notification plugin

1. A service (e.g. `submissions`) publishes `challenge.solved` to `events.Bus` after commit.
2. The bus fans out asynchronously to subscribers. A Discord notifier plugin receives the
   event over gRPC (`Notify`) and posts a message. Delivery is best-effort; a slow/failing
   notifier is logged and does not block or fail the solve.

### CLI / MCP operation

1. `osctf` (or the MCP server) authenticates with an **API token** (`Authorization: Bearer`).
2. It calls `/api/v1/...` exactly as the dashboard would — same handlers, same authz. No
   privileged backdoor exists for the CLI.

## What is deliberately absent (still)

- No in-process or WASM plugins; no plugin marketplace; no non-Go plugin SDK.
- No runtime/storage plugins (in-tree in v0.3).
- No durable event queue (the bus is in-memory, best-effort).
- No syscall sandbox around plugins (process isolation only; documented limitation).
