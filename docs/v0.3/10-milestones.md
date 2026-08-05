# 10 — Milestones (Build Plan)

Execute in order on top of the shipped `v0.2.0` code. Each milestone lists **tasks**,
**deliverables**, and **acceptance** — commands/checks that must succeed (from the repo
root) before moving on. The overriding invariant every milestone preserves: **a no-plugin
deployment passes the v0.2 suite unchanged and `/api/v0` keeps answering.**

Dependencies are linear except where noted.

---

## M0 — Contracts: API v1, the plugin proto, tokens

**Tasks**: bump `openapi.yaml` to `1.0.0`, add `/api/v1` as the canonical server (+ `/api/v0`
deprecated), mount the generated server under both prefixes with `Deprecation`/`Sunset` on
v0; author the plugin `.proto` ABI ([`02-plugin-abi.md`](02-plugin-abi.md)) + `make generate`
wiring for `pluginpb` (host) and the SDK stubs; migration `0003` (`api_tokens`); `tokens`
package + Bearer middleware ([`06-api-v1.md`](06-api-v1.md)); new config env
([`03-plugin-loader.md`](03-plugin-loader.md)). Regenerate `apigen` + TS + `pluginpb`; new
endpoints return 501. (The Go API-v1 *client* is generated in [v0.3.1](../v0.3.1/README.md),
where the CLI that consumes it is built — not here.)

**Acceptance**:
```
make generate && git diff --exit-code                 # drift clean (openapi + proto)
goose ... up && down-to 0 && up                        # 0003 up/down/up
cd api && go build ./... && go test ./internal/tokens/... ./internal/config/...
curl /api/v1/event and /api/v0/event                   # both answer; v0 has Deprecation header
```
Backwards check: the dashboard still works against `/api/v0`; v0.2 suite green.

## M1 — Plugin host: loader, ABI transport, registries, event bus

**Tasks**: `plugin` package — go-plugin handshake, gRPC client stubs, `Loader`
(discover/validate/launch/`Info`/register/health/restart/shutdown) per
[`03-plugin-loader.md`](03-plugin-loader.md); `plugin/sdk` skeleton + `plugin/plugintest`
harness. Make the registries mutable: `auth.Registry`, `scoring.Register`, the challenge-type
registry; add `events.Bus` (async fan-out). Wire the loader into `serve`; built-ins
self-register first (override-protected).

**Acceptance**:
```
cd api && go test ./internal/plugin/... ./internal/events/...
```
Unit: manifest parse (good + every malformed → skip, host survives); ABI-major mismatch
refused; registry override protection; config/secret precedence; supervision via an
in-process fake (timeout→504, crash→502+restart, fallback rules); bus fan-out +
slow/failing subscriber isolation + no-secret payload scan.

## M2 — Prove scoring + notification (simplest types)

**Tasks**: route `scoring.Value` through the registry; a scoring plugin mode is valid at
authoring time and falls back to `static` on failure. Add the event-bus **emit points**
(publish after commit in `users`/`teams`/`events`/`challenges`/`submissions`/`scheduler`)
per the payload table in [`04-plugin-interfaces.md`](04-plugin-interfaces.md); the
`Notification` service + subscription. First-party plugins `linear-decay` + `webhook`
([`05-first-party-plugins.md`](05-first-party-plugins.md)) built via `scripts/build-plugins.sh`.

**Acceptance**:
```
bash scripts/build-plugins.sh
cd api && go test -run Contract ./api/plugins/linear-decay/... ./api/plugins/webhook/...
cd api && go test ./internal/scoring/... ./internal/submissions/...   # registry routing + solve→event
```
Contract (real binaries through the loader): score vectors match; a `challenge.solved` event
POSTs to a local sink; a hung sink still lets the solve complete. Boundary check: neither
plugin imports `internal/*`.

## M3 — Prove auth + challenge-type

**Tasks**: extend `AuthProvider` with the redirect capability + `auth.Registry`; the
provider-agnostic `/api/v1/auth/{provider}/login|callback` routes, identity→user mapping +
provisioning policy (open/invite/off), `GET /auth/providers`. Challenge-type registry +
`ValidateConfig` at authoring (admin + seeder; the CLI's offline `validate` is
[v0.3.1](../v0.3.1/README.md)) and `CheckFlag` inside the submissions tx. First-party `oidc`
+ `regex-flag`.

**Acceptance**:
```
cd api && go test ./internal/auth/... ./internal/challenges/... -run 'Registry|Provider|Type'
cd api && go test -run Contract ./api/plugins/oidc/... ./api/plugins/regex-flag/...
cd api && go test ./internal/handlers/... -run 'OIDC|ChallengeType|Provision' # integration
```
Integration: mock-OIDC `Begin`/`Complete` → session; provisioning policies enforced; a
`regex-flag` challenge authored + solved with the audit log/attempt counting identical to a
static challenge; email login unaffected when `oidc` is down.

## M4 — Wire the API: tokens, plugin admin, dashboard to v1

**Tasks**: implement token CRUD (`createToken`/`listToken`/`revokeToken`/`adminListTokens`),
plugin admin (`adminListPlugins`/`adminReloadPlugin`), `listAuthProviders`. Migrate the
dashboard client to `/api/v1`; add the profile **API-tokens** section, an **admin Plugins**
page (health/reload), and provider **login buttons**.

**Acceptance**:
```
cd api && go test ./internal/handlers/... -run 'Token|Plugin'   # integration
cd dashboard && npm run lint && npm run typecheck && npm test && npm run build
```
Integration: this is the **success-criterion-3 proof** — drive the full event lifecycle
(authenticate → set the event window → author + publish a challenge → start an instance →
submit → read the scoreboard → an admin action) against `/api/v1` with `Authorization:
Bearer`, using a plain HTTP client (test/`curl`, **no OSCTF client binary**), asserting **no
`Set-Cookie` and no session cookie** on any request. Plus: scope enforcement (`read` can't
POST; `admin` scope ≤ role); revoke invalidates immediately; `GET /admin/plugins` healthy;
reload works.

> The `osctf` CLI and MCP server (former M5 + M6) moved to
> [v0.3.1 milestones](../v0.3.1/04-milestones.md). v0.3 ends at the milestone below.

## M5 — Template, e2e, CI, docs, release

**Tasks**: the plugin **template repo** + author docs + its `AGENTS.md`
([`11-plugin-template.md`](11-plugin-template.md)); e2e (mock-OIDC login, admin Plugins page,
token management) added to the compose stack with `oidc` + `webhook` loaded; confirm all CI
jobs green (drift incl. proto, lint, test, integration, **plugins**, web, image, smoke,
e2e); update `CHANGELOG.md` (v0.3.0), `.env.example`, README/AGENTS, and the API stability
policy in `info.description`. Tag `v0.3.0`. (The `cli` job ships with the CLI in
[v0.3.1](../v0.3.1/README.md).)

**Acceptance**:
```
cd api && go test ./... -race && go test -run Contract ./api/plugins/...
cd dashboard && npm run lint && npm run typecheck && npm test && npm run build
docker compose up -d --build --wait && npx --prefix dashboard playwright test
# the exit-criterion self-verifiable gate (00-overview) — no CLI:
#   clean template checkout, CORE READ-ONLY  →  make build   (must NOT need to touch core)
#   make package  →  drop into OSCTF_PLUGINS_DIR  →  restart serve
#   GET /api/v1/admin/plugins shows it healthy  →  use it end to end (login/score/notify/solve)
#   with ZERO edits to the osctf/platform tree
```
The v0.1/v0.2 golden flows pass **unchanged**; the plugin, token, and provider-login flows
pass. All success criteria in [`00-overview.md`](00-overview.md) are met. Then tag + release.

---

## Milestone → success-criterion map

| Criterion ([00](00-overview.md)) | Proven in |
|---|---|
| 1 — third-party plugin used end-to-end | M2/M3 (contract) + M5 (exit-criterion gate) |
| 2 — kill a plugin → graceful degrade + restart | M1 (isolation) + M4 (admin health) |
| 3 — every dashboard op reachable via `/api/v1` + token, no cookie | M4 (Bearer HTTP-driven integration test; no client binary) |
| 4 — `/api/v0` alive; v0.2 suite passes plugin-free | M0 + every milestone's backwards check |
| 5 — ABI version enforced | M1 (mismatch refused) |

> The former criterion 4 (agent runs an event via MCP only) moved with the MCP server to
> [v0.3.1](../v0.3.1/04-milestones.md) as its success criterion 1.

## Notes for the building agent

- **Contracts first (M0).** Everything generates from the OpenAPI + the `.proto`; get both
  drift gates green before logic.
- **Prove the boundary early.** The first plugin (M2) must live outside `internal/` and pass
  the no-`internal`-import check — if it can't, the ABI is missing something; fix the ABI.
- **Registries default to the built-in.** Every change must leave a no-plugin deployment
  identical to v0.2 — the standing backwards-compat gate.
- **Secrets discipline extends to tokens + plugin config.** The leak-scan test covers token
  values, plugin secrets, and event payloads. No TODOs in code.
