# 09 — Testing & CI

Same philosophy and the same base jobs as v0.1/v0.2 — v0.3 **adds** tests and jobs, it does
not restructure the pipeline. The overriding invariant: **a no-plugin deployment passes the
entire v0.2 suite unchanged**, and `/api/v0` keeps answering. Baselines:
[`../v0.1/11-testing-ci.md`](../v0.1/11-testing-ci.md),
[`../v0.2/09-testing-ci.md`](../v0.2/09-testing-ci.md).

Existing jobs: `generate-drift`, `api-lint`, `api-test`, `api-integration`, `web`, `image`,
`smoke`, `e2e`. v0.3 extends `generate-drift` (now also proto + Go API client), `api-test`/
`api-integration` (loader, tokens, event bus, registries), adds a **`plugins`** job and a
**`cli`** job, and extends `smoke`/`e2e` with plugin + token + v1 coverage.

## New unit tests (`api-test`, no external processes)

| Area | Tests |
|---|---|
| `plugin` loader | Manifest parse/validate (good + every malformed case → skip, host survives). ABI-major mismatch refused. Registry registration + built-in override protection. Config/secret resolution precedence. |
| `plugin` supervision | With a **fake in-process plugin double** (a Go impl of each service): call timeout → 504 mapping; simulated crash → 502 + restart with backoff; fallback-to-default for scoring/challenge-type; no-fallback for auth. |
| `tokens` | PAT format, hashing, constant-time lookup, scope ≤ role, expiry + revoke invalidation, ban disables tokens. |
| `events` bus | Publish → async fan-out; slow subscriber doesn't block publisher; failing subscriber is logged, others still receive; payloads contain no secret fields (scan). |
| `auth` registry | Provider lookup; redirect Begin/Complete state/CSRF; identity→user mapping + provisioning policies (open/invite/off). |
| `scoring`/`challenges` | Registry resolution through a plugin mode / plugin type; unknown falls back; `ValidateConfig`/`CheckFlag` routing. |

The **secret-leak scan** extends to token values and plugin config: a test greps serialized
API responses, logs, audit meta, and event payloads for token/secret patterns and fails on
a hit.

## Plugin contract tests (`plugins` job, real subprocess)

The heart of the extensibility guarantee. A reusable **harness** (`plugin/plugintest`) boots
a plugin **binary** through the real loader (go-plugin, real gRPC) and asserts its behaviour
against the ABI — the same way the host will use it in production. Each first-party plugin
([`05-first-party-plugins.md`](05-first-party-plugins.md)) has a contract test:

- `linear-decay`: score vectors match; determinism.
- `webhook`: a `challenge.solved` event produces the expected POST to a local sink; a hung
  sink still lets the (simulated) solve complete.
- `regex-flag`: `ValidateConfig` rejects a bad regex; `CheckFlag` matches/rejects correctly.
- `oidc`: `Begin`/`Complete` against a mock IdP (dex or a stub) yield a valid `Identity`.

Plus a **boundary test**: a static check that no plugin package imports
`github.com/osctf/platform/internal/*` (grep/`go list` in CI) — proving the plugin boundary
is real. And an **isolation test**: kill a plugin process mid-call and assert the host maps
it to 502, stays up, and restarts the plugin.

## Integration tests (`api-integration`, testcontainers + real Postgres/daemon)

- **Loader end-to-end:** boot the platform with the `regex-flag` + `webhook` plugins present;
  `GET /api/v1/admin/plugins` shows them healthy; author + solve a `regex-flag` challenge
  via the API; a solve fires the webhook (local sink).
- **API tokens:** create a token, drive participant + admin flows with `Authorization:
  Bearer` and **no cookie**; scope enforcement (`read` can't POST; `admin` scope on a
  non-admin user grants nothing); revoke invalidates immediately.
- **v0/v1 parity:** the same request under `/api/v0` and `/api/v1` returns equal bodies; v0
  carries `Deprecation`/`Sunset` headers.
- **Backwards-compat gate:** the full v0.2 integration suite runs with **no plugins** and
  passes unchanged.

## CLI tests (`cli` job)

- Build `osctf`; run against a compose stack (or httptest server) with a token.
- Golden-path: `login → whoami → challenge validate → challenge create → instance start →
  submit → scoreboard`, asserting `--json` shapes and **exit codes** (0/2/3/4/5/6/7).
- Offline: `challenge validate` on a good and a bad `challenge.yaml` (exit 0 / 6) with no
  server; `challenge package` produces a deterministic tarball.
- The MCP server: a minimal MCP client lists tools (scope-gated), calls `get_scoreboard`
  (read) and a `confirm`-gated destructive tool, asserting the confirmation guard.

## e2e (`e2e`, Playwright)

Extend the compose stack to include the `oidc` (mock IdP) + `webhook` plugins and add:

- **Plugin login flow:** the login page shows the provider button (`GET /auth/providers`);
  clicking it completes the mock-OIDC round-trip and lands authenticated — proving a login
  the core has no code for.
- **Admin plugins page:** the new Instances-style admin page lists loaded plugins with
  health.
- **API-token management:** create a token in the profile UI, see it once, revoke it.

Keep the v0.1/v0.2 golden flows unchanged (backwards-compat gate). Reuse the retries:0 /
poll-for-propagation discipline from v0.2.

## Smoke (`smoke`)

Extend `scripts/smoke.sh` with: create + use an API token (Bearer, no cookie); assert
`GET /api/v1/admin/plugins` lists a loaded plugin; assert `/api/v0` still answers with a
`Deprecation` header. Guard plugin assertions to degrade gracefully if `OSCTF_PLUGINS_ENABLED=false`.

## CI job summary

| Job | Change |
|---|---|
| `generate-drift` | Now also regenerates `pluginpb` (proto) and the Go API-v1 client; fails on drift. |
| `api-lint` | Unchanged; new packages must pass. |
| `api-test` | Runs loader/tokens/bus/registry unit tests + the secret-leak scan. |
| `api-integration` | Loader e2e, token flows, v0/v1 parity, v0.2 backwards-compat gate. |
| **`plugins`** (new) | Builds first-party plugins; runs contract + boundary + isolation tests. |
| **`cli`** (new) | Builds `osctf`; golden-path + offline + MCP tests. |
| `web` | Dashboard migrated to `/api/v1`; token UI + plugins page tests. |
| `image` | Now also builds the plugin binaries into the image (or a plugins layer). |
| `smoke` | Token + plugins + v0-deprecation legs. |
| `e2e` | Mock-OIDC login, plugins page, token management. |

## Decision log

- **Contract tests run the real plugin binary through the real loader.** Anything less
  wouldn't prove the process boundary or the ABI; the harness is reused by third-party
  plugin authors (it ships with the template repo).
- **A CI boundary check forbids `internal/*` imports from plugins.** Mechanically enforces
  "plugins don't touch core."
- **v0/v1 parity is a test, not a hope.** The deprecation alias is verified, not assumed.
- **The v0.2 suite runs plugin-free and must pass.** The strongest backwards-compat proof.
