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

### Security — the auth return-path contract

Auth plugins are different, and get their own contract. The general plugin posture —
"trusted-but-isolated, no syscall sandbox" ([`00-overview.md`](00-overview.md)) — is fine
for scoring and notification plugins: the worst a hostile scoring plugin does is return
wrong numbers (the host falls back to `static`), and a hostile notifier leaks only the
non-secret event payload to a sink the operator already configured. But an **auth plugin
decides who gets in — and, transitively, who is an admin — in a security product.** So the
core does not trust its return value; it validates it on the way back.

An auth plugin's only output is an assertion: "the external identity `{subject, email, …}`
authenticated." Everything after that is the **core's** decision. On the return path from
`Complete`, the core enforces:

1. **A plugin cannot mint an admin — or any elevated — session.** Provisioning a new user
   always creates the **lowest role** (participant), regardless of anything in the claim.
   No field of `ExternalIdentity` sets a role, and the core ignores any a hostile plugin
   invents.
2. **A plugin cannot assert an existing user's identity without a verified binding.** The
   claim maps to a local account only through a binding the *core* established and trusts:
   a stored `(provider, subject)` link created on a prior successful login, or — on first
   login under the `open` / `invite-only` policy — a match on a **verified** email/subject
   the provider is authoritative for. A bare "this is `user@example.com`" is not enough to
   assume that user's account: with no existing binding it provisions a **new** user (policy
   permitting) or is refused — it never takes over an existing one.
3. **A plugin cannot set roles directly.** Roles are core state, changed only by an admin
   through the API (`adminUpdate…`). No login path — plugin or built-in — writes a role. An
   `admin` exists because a human admin made them one, never because a login said so.
4. **A malformed or hostile claim fails closed.** An empty/missing `subject`; an unverified
   email where policy requires verification; a claim carrying `role`/`admin`/`user_id`
   fields; a `state`/CSRF mismatch on the callback; or any error from `Complete` → the login
   is **rejected** (no session, no provisioning), logged loudly with the provider name, and
   the provider is marked unhealthy if it is structurally misbehaving. The same ban/hidden
   checks that gate a password login gate this one. There is **no auth fallback**: a broken
   provider errors; the others still work.

**Say it plainly: a malicious auth plugin is equivalent to a compromised core.** The checks
above bound the blast radius — a hostile provider still cannot directly grant admin, cannot
silently take over an existing account without a binding the core itself minted, and cannot
write roles — but it sits *inside* the authentication trust boundary. It can authenticate
identities it controls, and because it *is* the shim for its identity source, it can log in
as anyone that source can vouch for; it is not sandboxed, and the return-path validation
limits damage without containing a determined hostile plugin. So **installing an auth plugin
is an operator trust decision on the same level as replacing the core binary**: vet its
source and supply chain the way you vet core, and do not load an auth plugin you would not
merge into core. This is why auth is the one plugin type whose return path the core
polices field by field rather than trusting.

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
  value; it must be **deterministic**. On plugin failure the host **fails closed by default**:
  the submission errors and the player retries — because falling back to `static` mid-event
  computes the board under two rules and makes recovery ambiguous
  ([`03-plugin-loader.md`](03-plugin-loader.md) → the per-type table). A `static` fallback is
  an explicit operator opt-in (`OSCTF_PLUGIN_SCORING_FALLBACK`); when it fires the core marks
  the submission `scored_by=fallback` so the board stays **exactly recomputable from the
  submission log**. The plugin is marked unhealthy either way.

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
**logged and counted** (`osctf_plugin_events_dropped_total{name,event}`) and dropped for that
event — it never blocks or fails the originating action (a solve still commits even if Discord
is down), but the drop is **never silent** (an operator can always see it). Not durable in
v0.3 (a missed event is missed); durability is a later concern.

> **Say it plainly, so nobody builds the wrong thing on it: the event bus is best-effort and
> non-durable — a notification plugin *will* miss events** (a subscriber that is down, slow,
> restarting, or overloaded, or a host restart between publish and delivery, simply loses
> those events; there is no queue, retry, replay, or ack). This is fine for **chat alerts,
> dashboards, and other lossy signals**. It is **not** a foundation for anything requiring
> completeness — **cheat detection, audit-log forwarding, billing, compliance export, or any
> "process every event" pipeline must not be built on the bus.** Those need a durable source
> (the DB / audit log) and a pull or reconciled feed, which is a post-v0.3 concern. If a
> plugin's correctness depends on seeing *every* event, the bus is the wrong interface.

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
- **A `CheckFlag` failure fails closed and does not cost the player.** If the plugin errors,
  times out, or dies mid-check, the submission errors and the tx **rolls back with no solve
  and no `max_attempts` decrement** — a plugin outage must not burn a player's attempts. The
  host never falls back to a different comparison: accept-anything would hand out free solves
  and reject-everything is indistinguishable from a wrong flag
  ([`03-plugin-loader.md`](03-plugin-loader.md) → the per-type table).
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
  plugin can never grant admin, set a role, take over an existing account without a
  core-minted binding, or bypass bans — the core validates the return path field by field
  (see *Security — the auth return-path contract*). Still, a malicious auth plugin is
  equivalent to a compromised core: installing one is an operator trust decision on par with
  replacing the core binary. Auth is the one plugin type whose return value is never trusted
  as given.
- **The event bus is best-effort, non-durable, and non-secret by construction.**
  Notifications must never gate a user action or leak a secret; the payload schema enforces
  the latter. It is explicitly documented that a plugin *will* miss events, so nothing
  requiring completeness (cheat detection, audit forwarding, compliance) may be built on it —
  those need a durable, reconciled source, not the bus.
- **Challenge-type plugins verify, they don't run containers.** Keeps the security-critical
  submission flow and the runtime in core.
