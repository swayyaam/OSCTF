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

### Built-in login: override-protected *and* separately disable-able

Two orthogonal properties, decided here so an SSO-only deployment isn't stuck:

- **Override-protected (a security property, kept).** A plugin **cannot replace** the
  built-in `email` provider — `Registry.Register("email", …)` is refused without an explicit
  operator override. A plugin that could swap `email` would intercept every password login,
  which is exactly the auth return-path contract's concern. So plugins add providers beside
  the built-in; they never displace it.
- **Enabled by default, but disable-able (an operator control, distinct from the above).**
  "Cannot be overridden by a plugin" is **not** "cannot be turned off." Email/password login
  is **on by default** — a fail-closed **break-glass** path so a deployment always has a
  working login even if its auth plugin is down or misconfigured. An operator who wants
  SSO-only sets **`OSCTF_AUTH_EMAIL_LOGIN=false`**: `POST /auth/login` then returns a clear
  **`403`** ("email/password login is disabled on this deployment") and `email` is omitted
  from `GET /auth/providers`; only redirect providers (e.g. `oidc`) authenticate. This is
  what makes "SSO-only" possible without letting a plugin seize the built-in. **The platform
  refuses to boot** if email login is disabled and no other provider is registered — booting
  with no way to log in is worse than failing loudly. (Shipped in P1: the config, the login
  gate, and the boot check; `GET /auth/providers` and redirect providers arrive in P4.)
- **Break-glass caveat (documented for operators).** Disabling email while relying solely on
  an external IdP means an **IdP outage locks everyone out, admins included.** Operators who
  disable email should keep a break-glass path — re-enable `OSCTF_AUTH_EMAIL_LOGIN` via env
  in an emergency, or provision a bootstrap admin. The default (email on) is the safe choice;
  turning it off is a deliberate trade of that safety net for SSO exclusivity.

The disable config and the OIDC provider ship in **P4-auth**; the decision is fixed here so
the registry and the provider-list endpoint are built with it in mind.

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

**And the blast radius reaches past OSCTF.** A plugin with the `password` capability receives,
by design, the *plaintext credential a user typed* for that provider (`AuthenticateRequest.secret`
— the host cannot validate a credential without giving it to the validator). Users reuse
passwords across services, so a malicious password-capability plugin doesn't merely compromise
this OSCTF instance — it **harvests credentials that work elsewhere** (the corporate directory,
email, whatever the user reused). That is the concrete reason the "would you merge it into core"
test is the right bar: you are handing the plugin the same plaintext your login form collects.
Two consequences, both enforced:
- **The `password` capability is made visible, not buried in a manifest.** The loader logs it at
  startup (`plugin <name> loaded with the password capability — it will receive plaintext
  credentials`) and the admin plugin view flags it, so trusting it is an *informed* decision, not
  one that lives only in a YAML file someone read once.
- **The host must not leak the credential either.** A submitted credential is a secret on the
  host side of the boundary exactly like a flag or a token: it must never reach a log, an error
  body, an audit row, or a metric on the plugin-auth path. The flag-containment scanner covers
  it (a submitted credential is swept as a secret allowed to no one). The plugin sees it by
  design; the host leaking it would be a bug.

## 2. Scoring — locked at solve, recorded, read off the plugin

A plugin-scored challenge is valued **once, at solve time**, and the value is **recorded on
the solve** (`submissions.scored_value` / `scored_by`, migration `0007`). The scoreboard reads
that record; it never calls the plugin. So the board is **exactly recomputable from
`(submission log + records)` at every instant, regardless of plugin state** — a plugin that is
down, slow, or removed cannot change a served board, because the value each solve was worth is
already written down. There is no retroactive decay for plugin modes (each solve keeps the value
locked at its own solve time); built-in `static`/`dynamic` are unchanged and recompute from the
formula.

### The read path structurally cannot reach a plugin

This is enforced by construction, not by a well-behaved branch:

- A plugin scoring mode is registered in `scoring.Registry` **only as a marker**
  ([`cmd/platform/plugin_registrar.go`](../../api/cmd/platform/plugin_registrar.go),
  `scoringMarker`) — its sole job is to make write-time validation
  (`scoring.IsRegisteredMode`, used by challenge authoring) accept the mode. The marker's
  `Value()` is a static fail-safe; it holds no reference to the plugin.
- The plugin **process** is reachable only through the *separate* write-path scorer
  (`pluginScorer`, which holds the gRPC `Caller`s). The registry and the read path have no
  handle on it.
- `compute()` — the one scoreboard recompute — classifies each solve with
  `scoring.IsBuiltinMode` (a **pure constant check**: `static`/`dynamic` vs everything else, no
  registry lookup, no I/O) and reads `scored_value` for plugin modes. It has no code path that
  calls a plugin.

So "the scoreboard calls a plugin" is **unrepresentable**, not merely untested — the same class
of improvement as the WS hub serializing `apigen.Scoreboard` directly instead of trusting two
structs to stay in field order. Making the wrong thing impossible to write beats testing that it
doesn't happen. The soak's `-recompute-via-plugin` control deliberately re-introduces a plugin
call in the *recompute* to show the invariant it would break; and a full **plugin-outage** run
proves the served board keeps matching from-scratch with the plugin actually stopped mid-run.

### Fallback, pending, and missing

A scoring failure never fails the submission — the solve commits and correctness is unaffected
(that is the challenge-**type** plugin's job, §1). Only the *value* is affected, and it is always
recorded as one of three definite states in `scored_by`:

| `scored_by` | when | `scored_value` | board reads |
|---|---|---|---|
| `<mode>` | the plugin returned a value | the value | that value |
| `fallback` | plugin down, **fallback on** | static value | that value |
| `pending` | plugin down, **fallback off** | `NULL` | `0`, until backfilled |

- **`pending` is a state, not a failure.** It is the honest, expected record of a solve that
  landed while the plugin was unreachable and fallback was off: a definite "value deferred,
  resolves to `0` for now." It self-heals — the repair worker fills the real value when the
  plugin returns.
- **A *missing* record is a durability failure.** Both columns `NULL` on a valid plugin solve
  means the post-commit write never landed. It also resolves to `0` on the board, but it is an
  **absence**, not a decision — so it is counted **separately** and is **alertable**
  (`osctf_plugin_scores_missing` is an alarm; `osctf_plugin_scores_pending` is a status). Keeping
  the two apart is what lets the missing gauge mean "the write path is broken," not "a plugin is
  down."

### Off-read-path repair

A background worker (`submissions.ScoreRepairer`, ~10 s tick = the backfill-latency bound) fills
`pending` and `missing` records with the plugin's value, or re-records `pending` while the plugin
is still down. It runs **off the read path on purpose**: repairing inline on a scoreboard read
would let a live recompute and an independent recompute resolve the same absent record
differently depending on whether the plugin happened to answer — the non-determinism the whole
design exists to avoid.

### Fallback-on is the recommended default

`OSCTF_PLUGIN_SCORING_FALLBACK` defaults **on**. The earlier worry — that a `static` fallback
mid-event computes the board "under two rules" and makes recovery ambiguous — is dissolved by the
record: each solve's row states exactly what it was worth (`<mode>`, `fallback`, or `pending`), so
the board is exactly recomputable no matter which rule produced which solve. Fallback-on simply
avoids a participant-visible `0`; fallback-off (records `pending`) is for deployments that must
never show a value the plugin did not produce. A scoring plugin is called with
`(initial, min, decay, solves, params)` and must be **deterministic**; it is marked unhealthy on
failure either way.

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

`type` is a **new column, orthogonal to `kind`** — an additive migration defaulting existing
rows to the built-in type id, existing rows untouched. The two are different axes: **`kind`
is how the platform deploys** a challenge (standard vs container), **`type` is who decides
correctness** (the built-in comparison vs a plugin checker). A container challenge with a
plugin-provided checker needs both, so one field can't express it; reusing `kind` would
conflate deployment with verification. In P1 the registry only carries the type id and
`ValidateConfig`; `CheckFlag` does not enter the submission transaction until a plugin needs
it (P4).

```go
// A challenge-type plugin (SDK: sdk.Checker). ValidateConfig runs at author time; CheckFlag
// (optional) decides correctness inside the host's submission flow.
type ChallengeType interface {
    ID() string                                   // "standard" | "container" | plugin id
    ValidateConfig(cfg map[string]string) ConfigValidation
    CheckFlag(submitted string, cfg, instance map[string]string) (correct bool, err error)
}

// ConfigValidation is ONE reconciled shape spoken by the wire (ValidateResponse), the host
// (challenges.ConfigValidation), and the SDK (sdk.ConfigValidation) alike — previously these three
// disagreed. OK reports the config is usable; FieldErrors is one message per rejected field (→ the
// 422 errors map an admin sees); Normalized is the canonicalised config the host stores in place of
// the author's raw input (when empty, the input is stored unchanged).
type ConfigValidation struct {
    OK          bool
    FieldErrors map[string]string
    Normalized  map[string]string
}
```

- The built-in `standard`/`container` behaviour is registered as the default type; a
  challenge references a type id.
- **Per-challenge `type_config` (v0.3).** A challenge carries an author-defined `type_config`
  map — a **shared** `jsonb` column (challenge-type config now, the reserved scoring
  `sdk.Score.Params` later; never a second per-challenge config column). On create/update the
  host **dials** the type's `ValidateConfig(type_config)`: `OK=false` rejects the save with
  per-field `422` errors the admin sees; the `Normalized` result is stored in place of the raw
  input (empty ⇒ input stored unchanged). The dial fails **closed** — a type whose plugin can't
  be reached refuses the save (503) — but **only when `type_config` is being changed**, so a
  down plugin never blocks an unrelated edit (e.g. a title change). At submit time the stored
  `type_config` is passed to `CheckFlag`. Locked-at-solve is unaffected: editing `type_config`
  changes only how FUTURE submissions are judged. `type_config` is admin-only and MAY CONTAIN
  CHALLENGE-SENSITIVE DATA (a regex revealing flag structure) — it never rides a participant DTO,
  and plugins must never log it.
- **An unregistered type is rejected at write time**, with an error naming it — a `422` like
  `unknown challenge type "regex-flag": no such type is registered`. An admin typo, or a
  challenge authored against a plugin *this* deployment doesn't have, fails on create/update
  rather than silently storing a `type` nothing can resolve. (The default `standard` is always
  registered, so v0.2-shaped authoring is unaffected.)
- **`type` and `type_config` are admin-visible, participant-omitted.** Authors set and see them
  on the admin challenge DTO; they never appear on any participant-facing DTO (board, detail,
  WS). A new column on a table whose participant view is protected by field separation is exactly
  where that protection gets accidentally widened, so the serialization goldens and the
  flag-containment scanner both assert the absence of `type` and `type_config` (each with a
  sentinel value) from participant responses.
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

### An unresolvable type fails closed (specified now, implemented when reachable in P4)

Write-time rejection stops a *new* challenge from referencing a missing type, but an
*existing* challenge can become unresolvable later: its plugin is removed, the plugin is in
`failed`/quarantine, or a database is restored onto a deployment that lacks the plugin. P1
cannot reach this state (no plugin types exist yet), but **P4 can, and "the challenge exists
and nothing can score it" will happen in production** — so the behaviour is fixed here:

- **The challenge stays in the database** — never deleted or mutated because its type is
  momentarily unresolvable (the plugin may come back).
- **It is hidden from participants** — omitted from the board and 404 on direct access, the
  same as an unreleased challenge, so no one can attempt something that can't be scored.
- **Submissions against it are rejected with a clear error** (e.g. `this challenge's type is
  currently unavailable`), the tx rolls back with no solve and no attempt consumed. The host
  **does not** fall back to the standard flag comparison — that is the challenge-type
  equivalent of the scoring fallback we already rejected, and worse: it would **hand out
  solves against a checker that never ran**.
- **It is surfaced in the admin view as unresolvable** — the same treatment unadopted
  containers/networks got in v0.2 (a flagged, admin-visible state with the offending type
  named), so an operator can see and fix it.

This is the fail-closed posture applied consistently: an unresolvable checker withholds
scoring rather than guessing at it.

### Changing a challenge's type after solves exist is rejected (specified now, reachable in P4)

`ChallengeAdminUpdate` carries `type`, so an admin can change which checker decides a
challenge's correctness. P1 cannot reach the hazard — the two built-ins (`standard`,
`container`) share the same flag-comparison path, so switching between them changes nothing
about how a solve was decided. **P4 can:** once a plugin checker exists, changing the type of
a challenge that *already has valid solves* rewrites history in one of two unacceptable ways —
either those solves are silently invalidated, or they stand while attributed to a checker that
never scored them (the standing was earned under the old checker, the challenge now claims a
different one).

So the behaviour is fixed here, consistent with the unresolvable-type case above:

- **A type change is rejected when the challenge has at least one valid solve**, with a clear
  error (e.g. `cannot change the type of a challenge with existing solves; delete and recreate
  if you mean to re-score it`). The check is the solve count the update path already loads —
  zero valid solves, the change is allowed; one or more, it is refused at write time with the
  offending transition named.
- **An admin who genuinely means to re-score deletes and recreates**, which is already the
  explicit, audited, confirm-gated path for rewriting a solved challenge's history (the same
  guard that makes deleting a visible challenge mid-event require `confirm=true`). Making the
  destructive intent explicit is the point — a type change must not be a quiet side door
  around it.
- **This is a write-time application check, not a DB constraint** — same reasoning as the
  absent `type` CHECK: the rule depends on runtime state (the solve count), which a schema
  constraint cannot express.

Implemented when a second checker becomes reachable (P4); until then the built-ins make it
unobservable, but the rule is settled so P4 doesn't have to relitigate it.

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
