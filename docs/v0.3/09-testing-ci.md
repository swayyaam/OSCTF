# 09 — Testing & CI

Same philosophy and the same base jobs as v0.1/v0.2 — v0.3 **adds** tests and jobs, it does
not restructure the pipeline. The overriding invariant: **a no-plugin deployment passes the
entire v0.2 suite unchanged**, and `/api/v0` keeps answering. Baselines:
[`../v0.1/11-testing-ci.md`](../v0.1/11-testing-ci.md),
[`../v0.2/09-testing-ci.md`](../v0.2/09-testing-ci.md).

Existing jobs: `generate-drift`, `api-lint`, `api-test`, `api-integration`, `web`, `image`,
`smoke`, `e2e`. v0.3 extends `generate-drift` (now also the plugin **proto**), `api-test`/
`api-integration` (loader, tokens, event bus, registries), and extends `smoke`/`e2e` with
plugin + token + v1 coverage.

> **No separate `plugins` job — and why.** This doc originally added one, because the
> first-party plugins were expected to live in-tree. They do not: each reference plugin is its
> own repository ([`05-first-party-plugins.md`](05-first-party-plugins.md)), so its contract
> test runs in ITS CI, against the published SDK — which is the stronger check, since a plugin
> built inside this repo could lean on core sources and hide exactly the gaps the exit criterion
> exists to catch. What the platform owes CI is that the AUTHOR SURFACE works, and that runs in
> `api-test`: the contract harness builds an example plugin of each of the four types from
> `plugin/sdk/contract/testdata` and verifies it through a real subprocess, plus a static
> boundary check. Adding a job that re-ran the same tests under a different name would be
> ceremony. (The **`cli`** job — CLI golden
path + offline + MCP — ships with the CLI in [v0.3.1](../v0.3.1/03-testing-ci.md); the Go
API-v1 *client* it depends on is generated there too.)

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

## Plugin contract tests (`api-test`, real subprocess)

The heart of the extensibility guarantee. A reusable **harness** (`plugin/plugintest`) boots
a plugin **binary** through the real loader (go-plugin, real gRPC) and asserts its behaviour
against the ABI — the same way the host will use it in production. Each first-party plugin
([`05-first-party-plugins.md`](05-first-party-plugins.md)) has a contract test:

In-repo, one example plugin per type in `plugin/sdk/contract/testdata`, each built and driven
through the real loader by `Verify*` — the same path an out-of-tree author takes, with no
`internal/*` anywhere in the test file:

- `examplescorer` (`VerifyScoring`): score vectors match; determinism.
- `examplenotifier` (`VerifyNotification`): a subscribed event is delivered without a transport error.
- `examplechecker` (`VerifyChallengeType`): `ValidateConfig` rejects bad config; `CheckFlag`
  errors rather than returning false when it cannot decide.
- `exampleauth` (`VerifyAuth`): the authorize URL carries the HOST's state verbatim; an
  incompletable callback is refused rather than answered with an identity.

Each reference plugin repo runs the same `Verify*` against its own binary in its own CI.

Plus a **boundary test** (`plugin.TestPluginPackagesDoNotImportInternal`): a static check that
no public plugin package imports `github.com/swayyaam/OSCTF/internal/*` except the generated
`pluginpb`. It walks non-test sources only, because Go never compiles a dependency's test files
— what an external module actually compiles is the boundary that matters. This is not
hypothetical: `plugin/sdk` once imported the loader and the event package, so a hello-world
plugin linked the Postgres driver and the Prometheus stack, and the rule was being honoured by
the plugin repos while the SDK broke it underneath them. And an **isolation test**: kill a plugin process mid-call and assert the host maps
it to 502, stays up, and restarts the plugin.

**The loader concurrency & failure invariants** are pinned here too — each row of the
invariant table in [`03-plugin-loader.md`](03-plugin-loader.md) (*Invariants the tests pin*)
is a named test in this job or `api-test`: no orphaned process survives shutdown/`SIGKILL`
(pidfile boot sweep), no non-`ready` state serves, no registry entry for a stopped plugin, a
crash-loop is quarantined within the cap (bounded processes/fds), reload is idempotent, and
no host→plugin call blocks past its deadline. The residue guard (`goleak` + fd/PID count)
extends over a load→serve→stop cycle.

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

## CLI + MCP tests → v0.3.1

The `cli` job (CLI golden path, offline `validate`/`package` parity, and the MCP client
harness) moved with the CLI and MCP server to
[v0.3.1](../v0.3.1/03-testing-ci.md). v0.3's pipeline does not build `osctf`.

The **token-only, no-cookie** property those clients rely on is proven **here**, against the
API directly — the `api-integration` Bearer flow below and the `smoke` token leg — not
through any client binary. That is deliberate: it is an API guarantee (success criterion 3),
so the clients inherit it rather than re-prove it.

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
| `generate-drift` | Now also regenerates `pluginpb` (proto); fails on drift. (The Go API-v1 client is a v0.3.1 drift source.) |
| `api-lint` | Unchanged; new packages must pass. |
| `api-test` | Runs loader/tokens/bus/registry unit tests + the secret-leak scan. |
| `api-integration` | Loader e2e, token flows, v0/v1 parity, v0.2 backwards-compat gate. |
| **`plugins`** (new) | Builds first-party plugins; runs contract + boundary + isolation tests. |
| ~~`cli`~~ | Moved to [v0.3.1](../v0.3.1/03-testing-ci.md) with the CLI + MCP server. |
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
