# 04 — The Extensible Interfaces

This doc specifies the four core-side surfaces plugins hook into and how each built-in
becomes "just the default registration." The plugin-side gRPC shapes are in
[`02-plugin-abi.md`](02-plugin-abi.md); here we define the **host** contracts and the
in-core changes.

## 1. Authentication — from one provider to a registry

Today `handlers.Deps.Auth` is a single `auth.AuthProvider`
([`auth/provider.go:20`](../../api/internal/auth/provider.go#L20)) and the only login path
is email/password. v0.3 introduces a registry and generalizes the interface to support
redirect (OAuth/OIDC) providers.

```go
// AuthProvider (extended). Existing Name()/Authenticate() unchanged; redirect is optional.
type AuthProvider interface {
    Name() string
    Authenticate(ctx, identifier, secret string) (uuid.UUID, error) // credential providers
}
type RedirectProvider interface {                                   // optional, for OAuth/OIDC
    Begin(ctx, redirectURI string) (authorizeURL, state string, err error)
    Complete(ctx, params map[string]string, state string) (ExternalIdentity, error)
}

type Registry struct{ /* name -> provider (+ optional RedirectProvider) */ }
```

Core changes:

- `auth.Registry`: `email` self-registers (built-in, credential). The loader adds plugin
  providers (e.g. `oidc`, `github`).
- New host routes, provider-agnostic: `GET /api/v1/auth/{provider}/login` (→ `Begin` → 302)
  and `GET /api/v1/auth/{provider}/callback` (→ `Complete` → identity). The core owns
  state/CSRF, session issuance, and **identity → local user mapping** (find by verified
  email/subject, provision on first login per an admin policy: `open` / `invite-only` /
  `off`). Credential providers keep the existing `POST /auth/login`.
- The login page lists available providers (`GET /api/v1/auth/providers`) so the dashboard
  renders the right buttons.

Security: a provider only *asserts an external identity*; the core decides whether that
maps to a session, applies the same ban/hidden checks, and never trusts a plugin to set
roles. No auth fallback on plugin failure — a broken provider errors; others still work.

## 2. Scoring — the registry is already there

`scoring.Registry()` ([`scoring/engine.go:56`](../../api/internal/scoring/engine.go#L56))
is a `map[string]ScoringEngine`. v0.3 turns it into a **mutable** registry the loader adds
to, and `scoring.Value(mode, …)` resolves through it:

```go
scoring.Register("acme-linear", pluginEngine)   // loader, for a scoring plugin
// Value(mode) unchanged for callers; unknown mode still falls back to static.
```

- A challenge's `scoring` field may now be a plugin mode name (validated at authoring time:
  the mode must be a registered engine). Built-in `static`/`dynamic` are unchanged and
  protected from override.
- A scoring plugin is called with `(initial, min, decay, solves, params)` and returns the
  value; it must be **deterministic**. On plugin failure the host logs and falls back to
  `static` for that computation (a wrong-but-safe value beats a failed scoreboard render),
  and marks the plugin unhealthy.

## 3. Notifications — a new event bus

There is no event/notification system today. v0.3 adds `events.Bus` (distinct from the
event *window* `events.Service`): services publish typed domain events after their
transactions commit; subscribers (notification plugins) react asynchronously.

```go
type Event struct {
    Name       string            // "challenge.solved"
    ID         string            // uuid; for dedup
    OccurredAt time.Time
    Data       map[string]string // flat, NON-SECRET fields
}
type Bus interface {
    Publish(ctx, Event)                 // fire-and-forget; returns immediately
    Subscribe(name string, h Handler)   // "*" subscribes to all
}
```

Delivery model: **in-process, async, best-effort.** `Publish` enqueues and returns; a small
worker pool fans out to subscribers with per-delivery timeouts. A slow/failing subscriber is
logged and dropped for that event — it never blocks or fails the originating action (a solve
still commits even if Discord is down). Not durable in v0.3 (a missed event is missed);
durability is a later concern.

Canonical event names + payload keys (the notification contract — additive only):

| Event | Key `data` fields (all strings, non-secret) |
|---|---|
| `user.registered` | `user_id`, `username` |
| `team.created` | `team_id`, `name`, `captain_id` |
| `event.phase_changed` | `phase` (`pre`/`running`/`ended`) |
| `challenge.published` | `challenge_id`, `slug`, `title`, `category` |
| `challenge.solved` | `team_id`, `team_name`, `challenge_id`, `slug`, `points`, `first_blood` |
| `instance.spawned` / `instance.expired` | `team_id`, `challenge_id`, `instance_id` |
| `flag.shared` | `submitter_team_id`, `owner_team_id`, `challenge_id` |

**Never** put a flag, password hash, API token, session id, or email into `data`. The
`flag.shared` event carries team ids only, consistent with the v0.2 audit rule. Services
publish by adding one `bus.Publish(...)` after the existing commit — the emit points are
listed in [`10-milestones.md`](10-milestones.md).

## 4. Challenge types — authoring + verification, not a new runtime

Challenges today are `kind = standard | container`. v0.3 adds a **challenge-type registry**
so a plugin can define a new authoring shape and (optionally) a custom flag check, without
touching the container runtime.

```go
type ChallengeType interface {
    ID() string                                   // "standard" | "container" | plugin id
    ValidateConfig(cfg map[string]string) (normalized map[string]string, fieldErrs map[string][]string)
    // Optional: custom correctness check inside the host's submission flow.
    CheckFlag(submitted string, cfg, instance map[string]string) (correct bool, err error)
}
```

- The built-in `standard`/`container` behaviour is registered as the default type; a
  challenge references a type id. Author-time validation (admin create/update, the CLI
  `validate`, and the seeder) calls `ValidateConfig`.
- A type with a custom `CheckFlag` participates in the **existing** submissions transaction:
  the host still locks the challenge, enforces solved/attempt rules, rate limits, and
  logging (v0.1), and calls the plugin only to answer correctness — inside the same
  constant-time-discipline boundary. A type without `check` uses the core's standard flag
  comparison (static or v0.2 per-instance).
- Runtime/instancing is unchanged: a container challenge of any type still deploys through
  `ChallengeRuntime`. Challenge-type plugins do **not** provide a runtime.

## What stays in-tree (not pluginizable in v0.3)

- **`ChallengeRuntime`** (Docker) and **`ObjectStore`** (MinIO/S3): single implementations,
  wired directly. Their streaming/SDK shapes don't fit the process boundary and the K8s
  runtime is v0.4. The registries are designed so they *could* be added without an ABI
  break.
- The scoreboard, event window, teams, and submissions *flows* are core; plugins hook the
  four surfaces above, not the flow logic.

## Decision log

- **Registry per interface; built-in registers first and is override-protected.** Removing
  every plugin deterministically restores v0.2.
- **Auth providers assert identity; the core owns sessions, roles, and provisioning.** A
  plugin can never grant admin or bypass bans.
- **The event bus is best-effort and non-secret by construction.** Notifications must never
  gate a user action or leak a secret; the payload schema enforces the latter.
- **Challenge-type plugins verify, they don't run containers.** Keeps the security-critical
  submission flow and the runtime in core.
