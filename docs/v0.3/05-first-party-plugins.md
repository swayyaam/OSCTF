# 05 — First-party Reference Plugins

Four reference plugins, one per type, each proving its interface end to end. They live
**outside** the core packages (`api/plugins/<name>/` or a sibling module) and are loaded
exactly the way a third-party plugin is — no special path, no core imports beyond
`plugin/sdk` + `pluginpb`. They double as the worked examples the template repo
([`11-plugin-template.md`](11-plugin-template.md)) points at. Each ships an `AGENTS.md`.

Build output: `osctf/plugin-<name>` binaries + a `plugin.yaml` each; a
`scripts/build-plugins.sh` builds them, mirroring `build-examples.sh`.

## 1. `oidc` — OpenID Connect / OAuth auth

Proves the **auth** interface (redirect capability).

- **Config:** `issuer` (required), `client_id` (required), `client_secret` (required,
  secret), `scopes` (default `openid email profile`).
- **Begin:** discovers the IdP's authorize endpoint from `issuer/.well-known/
  openid-configuration`, generates PKCE + state, returns the authorize URL. The core's
  `/api/v1/auth/oidc/login` 302s there.
- **Complete:** exchanges the code at the token endpoint, verifies the ID token signature
  and `aud`/`iss`/`nonce`, returns an `Identity{subject, email, username, claims}`. The
  core maps `subject` (or verified `email`) to a local user, provisioning on first login
  per policy, then issues a session.
- **Proves:** a login the core has no code for; the login page shows a "Sign in with
  <IdP>" button next to email/password; email login still works if the plugin is down.
- **Test target:** a throwaway OIDC IdP (e.g. a mock/`dex` container) in the plugin's
  integration test; the platform e2e can stub `Begin`/`Complete` via a fake IdP.

## 2. `linear-decay` — an alternative scoring algorithm

Proves the **scoring** interface.

- **Config:** `step` (points lost per solve, default from `params`), floor at `min`.
- **Value:** `max(min, initial - step*solves)` — a linear curve distinct from the built-in
  quadratic `dynamic`. Deterministic.
- **Proves:** a challenge authored with `scoring: linear-decay` scores through a plugin;
  `static`/`dynamic` are untouched; on plugin failure the host **fails closed by default**
  (the submission errors and the player retries) and marks the plugin unhealthy. The
  `static` fallback is an operator opt-in (`OSCTF_PLUGIN_SCORING_FALLBACK`); when it fires
  the submission is marked `scored_by=fallback` so the board stays recomputable — see the
  per-type failure table in [`03-plugin-loader.md`](03-plugin-loader.md) and the scoring
  section in [`04-plugin-interfaces.md`](04-plugin-interfaces.md).
- **Test target:** a table of `(initial, min, step, solves) → value` vectors, run both
  against the plugin binary (contract test) and as a pure unit test of its logic.

## 3. `webhook` — Discord / generic webhook notifier

Proves the **notification** interface + the event bus.

- **Config:** `url` (required, secret — the webhook endpoint), `events` (comma list or `*`,
  default `challenge.solved,event.phase_changed`), `template` (optional message format).
- **Subscriptions:** returns the configured event names.
- **Notify:** formats the event's `data` into a message and POSTs it to `url` (Discord
  webhook JSON, or a generic JSON body). Times out fast; failures are logged, never
  propagated to the originating action.
- **Proves:** a solve triggers a Discord post with no core changes; a slow/broken webhook
  does not slow or fail submissions; no secret ever reaches the payload (the event carries
  ids/points only).
- **Test target:** a local HTTP sink asserts the POST body for a `challenge.solved` event;
  a hung sink proves the solve still completes.

## 4. `regex-flag` — a custom challenge type

Proves the **challenge-type** interface (validate + check).

- **Config:** `pattern` (a regex the submission must fully match), `flags` (e.g. `i` for
  case-insensitive).
- **ValidateConfig:** compiles `pattern` at authoring time; a bad regex is a field error
  surfaced in the editor / `osctf challenge validate`.
- **CheckFlag:** returns whether the submission fully matches `pattern`. Runs inside the
  host submission flow (solve/attempt/rate-limit/log all enforced by core).
- **Proves:** a challenge whose "flag" is a *pattern* (e.g. any valid license key) — a
  correctness rule the core doesn't have — authored and solved without core changes; a
  `standard` challenge is unaffected.
- **Test target:** author a `regex-flag` challenge via the API, submit matching and
  non-matching values, assert solve/no-solve and that the audit log + attempt counting
  behave exactly as for a static challenge.

## Cross-cutting requirements for all four

- Import only `plugin/sdk` + `pluginpb`; **zero** imports of `github.com/osctf/platform/
  internal/*`. This is enforced by a CI check (see [`09-testing-ci.md`](09-testing-ci.md)).
- Ship `plugin.yaml`, an `AGENTS.md`, and a `README.md`; build reproducibly via
  `scripts/build-plugins.sh`.
- Honour the secrets rule: secret config is env-only; nothing secret is logged or returned.
- Each has a **contract test** run against its real binary through the loader, and the
  platform e2e loads at least `webhook` + `regex-flag` to prove the end-to-end path
  ([`09-testing-ci.md`](09-testing-ci.md)).

## Decision log

- **One reference plugin per type, deliberately small.** They exist to prove the interface
  and seed the template, not to be feature-complete products (a full SSO suite is a
  post-1.0 ecosystem concern).
- **They live outside `internal/` and can't import it.** This is the litmus test that the
  plugin boundary is real — if a reference plugin needed a core internal, the ABI is
  missing something and *that* gets fixed.
- **`webhook` targets Discord's format but stays generic.** Covers the roadmap's
  "Discord/webhook notifications" with one plugin.
