# 03 — Testing & CI

Same philosophy and the same pipeline as v0.1–v0.3 — v0.3.1 **adds one job** (`cli`) and
touches nothing else. The overriding invariant is inherited: a no-plugin deployment passes
the v0.2 suite unchanged, `/api/v0` keeps answering, and the v0.3 plugin/token/v1 tests stay
green. This version's tests prove only that the two clients faithfully drive API v1.
Baseline: [`../v0.3/09-testing-ci.md`](../v0.3/09-testing-ci.md).

Because the clients add no server capability, **every assertion here is against a real
running API** (a compose stack or an `httptest` server), never a mock of the platform. A
client test that needs a stubbed server is testing the wrong thing.

## The `cli` job (new)

Builds the `osctf` binary and exercises it against API v1 with an API token. Three legs:

**Golden path (remote).** Against a running deployment, with a freshly minted token:

```
osctf login → whoami → challenge validate → challenge create →
instance start → submit → scoreboard
```

Assert the `--json` output shape of each command and the **exit code** at each step
(`0` success; `5` on a duplicate submit; `3` when the token lacks scope; etc.). This is the
executable form of v0.3.1 success criterion 2.

**Offline (no server).** With no network:

- `osctf challenge validate` on a good `challenge.yaml` → exit `0`; on a bad one → exit `6`
  with `{ ok:false, field_errors:{…} }`.
- `osctf challenge package` → a **deterministic** tarball (byte-identical across runs).
- **Parity:** the offline validator and the server's authoring validation agree on the same
  inputs — both go through the shared `challengespec` parser promoted in v0.3. A table test
  runs the same good/bad specs through the CLI and the seeder/API and asserts identical
  verdicts.

**MCP.** A minimal MCP client launches `osctf mcp` (stdio) with a token and asserts:

- `tools/list` is **scope-gated** — a `read` token surfaces only read tools; admin tools are
  absent, not merely refused.
- `get_scoreboard` (read) returns the standings.
- A destructive tool (`delete_challenge`, `destroy_instance`) is **refused without
  `confirm:true`** and echoes what it would affect.
- **No tool output contains a secret** — the leak scan (flags, token values, password
  hashes) from v0.1–v0.3 extends over MCP tool results.

```
cd api && go build ./cmd/osctf && go test ./cmd/osctf/...          # unit + golden
cd api && go test ./cmd/osctf/... -run MCP                          # MCP client harness
```

## What does not change

- **No new dashboard e2e.** The CLI and MCP server have no browser surface; the v0.3 e2e
  suite (mock-OIDC login, admin plugins page, token management) is unchanged.
- **`smoke` is unchanged.** The v0.3 smoke legs already create and use an API token over
  Bearer with no cookie — that is the property the clients rely on, and it is proven there
  against the API directly. v0.3.1 does not add a smoke leg; a broken client is caught by
  the `cli` job, not by the deployment smoke test.
- **`generate-drift` already covers the client.** The Go API-v1 client the CLI uses is
  generated from `openapi.yaml` by `make generate`; drift is caught by the existing gate.
  Adding the CLI does not add a new drift source beyond that already-generated client.

## CI job summary

| Job | Change |
|---|---|
| `generate-drift` | Unchanged — the Go API-v1 client it already regenerates is what the CLI imports. |
| `api-test` / `api-integration` | Unchanged. |
| `plugins` | Unchanged (v0.3). |
| **`cli`** (new) | Builds `osctf`; runs the golden path, the offline validate/package parity checks, and the MCP client harness. |
| `web` / `image` / `smoke` / `e2e` | Unchanged. |

## Decision log

- **Client tests run against a real API, never a platform mock.** The whole point of a pure
  client is that it exercises the real endpoint; a mock would prove nothing about API
  completeness.
- **Offline↔server validator parity is a test, not a hope.** One `challengespec` parser,
  asserted to give identical results in the CLI and on the server.
- **The token-only property is not re-tested here.** It is a v0.3 API guarantee (success
  criterion 3, the smoke Bearer leg); the clients inherit it by using tokens, so re-proving
  it in the `cli` job would duplicate the API's own gate.
