# 02 — Plugin ABI (go-plugin + gRPC)

The ABI is the stable contract between the host and a plugin. It lives in
`api/internal/plugin/proto/*.proto` and its generated Go stubs (`pluginpb`), both checked
in and drift-gated exactly like `apigen`. The transport is **HashiCorp go-plugin** (a
handshake + a local gRPC server the plugin serves, the host dials). This doc defines the
handshake, the versioning rule, and the protobuf service for each plugin type.

Baseline reading: [`01-architecture.md`](01-architecture.md) for why go-plugin/gRPC.

## Handshake & versioning

go-plugin's `HandshakeConfig` gates connection:

```go
var Handshake = plugin.HandshakeConfig{
    ProtocolVersion:  1,                 // ABI MAJOR — bumped only on a breaking change
    MagicCookieKey:   "OSCTF_PLUGIN",
    MagicCookieValue: "osctf-plugin-v1", // guards against launching a non-OSCTF binary
}
```

- **ABI major** = `ProtocolVersion`. A plugin built against a different major is refused
  by go-plugin before any call — the loader logs `plugin <name>: ABI major mismatch (host
  1, plugin 2)` and skips it. No crash.
- **ABI minor** is carried in the manifest (`abi: "1.3"`) and in an `Info` RPC every plugin
  serves. Minor is **forward-compatible**: the host may call a plugin advertising an
  older minor (it won't invoke fields/methods the plugin lacks); a plugin advertising a
  newer minor than the host is accepted but the host uses only what it knows.
- The ABI version is **independent of the HTTP API version** ([`06-api-v1.md`](06-api-v1.md)).
  A plugin never sees `/api/v1`; it sees only the ABI.

Every plugin serves a common `Info` method used at load time:

```proto
message InfoRequest {}
message InfoResponse {
  string name = 1;          // must match the manifest name
  PluginType type = 2;      // AUTH | SCORING | NOTIFICATION | CHALLENGE_TYPE
  string abi = 3;           // "1.<minor>"
  string version = 4;       // the plugin's own semver, for display
  repeated string capabilities = 5; // optional feature flags within the type
}
enum PluginType { AUTH = 0; SCORING = 1; NOTIFICATION = 2; CHALLENGE_TYPE = 3; }
```

## One go-plugin "plugin set", one service per type

The host registers a go-plugin `PluginSet` keyed by type; a plugin implements exactly the
one service its manifest `type` declares (plus `Info`). Errors cross the boundary as gRPC
`status` codes the host maps to domain errors (`INVALID_ARGUMENT`→400,
`UNAUTHENTICATED`→401, `PERMISSION_DENIED`→403, `UNAVAILABLE`→502, `DEADLINE_EXCEEDED`→504,
`UNIMPLEMENTED`→treat as "not supported", else 500). Secrets/PII must not appear in error
strings.

### Auth service

Covers both direct credential auth (like the built-in `email`) and redirect/OAuth flows.
The host owns the HTTP routes and session issuance; the plugin owns the identity logic.

```proto
service Auth {
  rpc Info(InfoRequest) returns (InfoResponse);

  // Direct credential auth (optional; capability "password").
  rpc Authenticate(AuthenticateRequest) returns (Identity);

  // Redirect/OAuth auth (optional; capability "redirect").
  rpc Begin(BeginRequest) returns (BeginResponse);      // -> redirect URL + opaque state
  rpc Complete(CompleteRequest) returns (Identity);     // callback params -> identity
}

message AuthenticateRequest { string identifier = 1; string secret = 2; }
message BeginRequest  { string redirect_uri = 1; }      // host's callback URL
message BeginResponse { string authorize_url = 1; string state = 2; }
message CompleteRequest { map<string,string> params = 1; string state = 2; }

// The external identity the plugin vouches for. The host maps subject -> local user
// (provisioning on first login per policy), then issues its own session.
message Identity {
  string subject = 1;        // stable IdP-unique id
  string email = 2;
  string username = 3;       // suggested handle
  map<string,string> claims = 4;
}
```

### Scoring service

Pure and stateless — the simplest type. The host passes the parameters and solve count; the
plugin returns the value. Determinism is required (same inputs → same output).

```proto
service Scoring {
  rpc Info(InfoRequest) returns (InfoResponse);
  rpc Value(ScoreRequest) returns (ScoreResponse);
}
message ScoreRequest  { int32 initial = 1; int32 min = 2; int32 decay = 3; int32 solves = 4; map<string,string> params = 5; }
message ScoreResponse { int32 value = 1; }
```

`params` carries any extra knobs a custom curve needs, declared in the plugin's config
schema. The `min`/`decay` fields mirror the built-in `ChallengeScoring` so simple curves
need no extra params.

### Notification service

Fire-and-forget. The host publishes a domain event; the notifier reacts. Return is an ack
only; the host never blocks a user action on a notifier.

```proto
service Notification {
  rpc Info(InfoRequest) returns (InfoResponse);
  rpc Subscriptions(InfoRequest) returns (SubscriptionList); // which events it wants
  rpc Notify(Event) returns (NotifyAck);
}
message SubscriptionList { repeated string event_names = 1; } // e.g. "challenge.solved"; "*" = all
message Event {
  string name = 1;                 // "challenge.solved"
  string id = 2;                   // unique event id (dedup)
  string occurred_at = 3;          // RFC3339
  map<string,string> data = 4;     // flat, non-secret fields (team, challenge, points, …)
}
message NotifyAck { bool handled = 1; }
```

Event `data` is **flattened, non-secret** strings only — never a flag, password hash, or
API token. The bus and the payload schema are defined in
[`04-plugin-interfaces.md`](04-plugin-interfaces.md).

### Challenge-type service

Defines a new challenge `type`: how its config is validated at authoring time, and
(optionally) a custom flag check. Rendering/runtime stays with the core; a challenge-type
plugin adds authoring + verification logic, not a new container runtime.

```proto
service ChallengeType {
  rpc Info(InfoRequest) returns (InfoResponse);
  rpc ValidateConfig(ValidateRequest) returns (ValidateResponse); // author-time
  rpc CheckFlag(CheckRequest) returns (CheckResponse);            // optional; capability "check"
}
message ValidateRequest  { map<string,string> config = 1; }
message ValidateResponse { bool ok = 1; map<string,string> field_errors = 2; map<string,string> normalized = 3; }
message CheckRequest  { string submitted = 1; map<string,string> config = 2; map<string,string> instance = 3; }
message CheckResponse { bool correct = 1; }
```

`CheckFlag` runs **inside the submissions flow's constant-time discipline on the host side**
of the boundary: the host still enforces solve/attempt rules and logging; the plugin only
answers "is this submission correct for this config/instance." A plugin without the
`check` capability falls back to the core's standard flag comparison.

## Generation & drift

- `.proto` is the source of truth; a `make generate` step runs `protoc`/`buf` to produce
  `pluginpb` (host stubs) and the SDK's plugin-side stubs. Both are checked in.
- `generate-drift` CI fails on any uncommitted diff, exactly as for `apigen`/`sqlc`.
- The SDK ([`plugin/sdk`](11-plugin-template.md)) re-exports the plugin-side stubs so an
  author imports one package, not raw protobuf.

## Decision log

- **One service per type, not one god-service.** Keeps each plugin minimal and its
  capabilities explicit; unimplemented optional RPCs return `UNIMPLEMENTED` and the host
  falls back.
- **Major via go-plugin handshake, minor via `Info`/manifest.** Major mismatch is refused
  before any call (no partial init); minor is forward-compatible so adding an optional RPC
  or field is non-breaking.
- **`CheckFlag` answers correctness only; the host keeps the security-critical flow.**
  A plugin can't see other teams' flags, bypass rate limits, or skip the audit log.
- **gRPC status → domain error mapping is fixed.** So plugin failures render as the same
  problem+json vocabulary as everything else.
- **Minimal payloads — every field is a forever commitment.** No plugin ever receives a flag,
  a password hash, or a session token. `CheckFlag` gets the submitted guess + author config +
  instance metadata; the host never injects the challenge's flag column or the per-instance
  generated flag. `Event.data` is non-secret AND non-PII (no email). The one deliberate,
  capability-gated exception is `Auth.Authenticate.secret` — the plaintext credential a user
  submitted for that provider (see the trust model in 04); adding a field later is a minor
  bump, removing one is a major, so start small.
- **`Identity.email` is plugin OUTPUT, not host PII.** The auth plugin returns its knowledge
  of the external subject for host-side provisioning; the host chooses whether to act on it.
  A plugin never *receives* an email from the host.
- **Request context: deadline via the gRPC context, not a message field.** The host sets a
  per-call deadline (gRPC propagates it, so a cooperative plugin can honour `ctx.Done()`) AND
  enforces it host-side — abandoning the call on expiry (`DEADLINE_EXCEEDED` → 504) and
  quarantining a plugin that repeatedly hangs. Cancellation is cooperative *and* host-side;
  the host never trusts the plugin to cooperate. A request id rides gRPC **metadata**
  (`x-osctf-request-id`) for log correlation, not a field on every message. There is **no
  tenant or CTF-event id** — OSCTF is single-event per deployment; multi-event would be a
  future major.
- **Codegen determinism is asserted, not hoped for.** `buf` (pinned) compiles; `protoc-gen-go`
  and `protoc-gen-go-grpc` (pinned) emit the Go. `make proto-version-check` fails loudly if any
  of the three differs from the pinned version, so a developer's toolchain can't silently drift
  the checked-in stubs before the generate-drift gate even runs.
