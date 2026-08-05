# 04 — Milestones (Build Plan)

Execute in order on top of a built **v0.3** (API v1 stable, `api_tokens` + Bearer auth
working, OpenAPI at `1.0.0`, the Go API-v1 client generated). Each milestone lists
**tasks**, **deliverables**, and **acceptance** — commands/checks that must pass from the
repo root before moving on. Nothing here changes server behaviour; the standing
backwards-compat gate (v0.2 suite green plugin-free, `/api/v0` answering, v0.3 plugin/token
tests green) holds throughout.

These are v0.3's former M5 (CLI) and M6 (MCP), lifted into their own version, plus a small
release milestone.

---

## M0 — The `osctf` CLI

**Tasks**: `api/cmd/osctf` (Cobra) with the command tree in [`01-cli.md`](01-cli.md); wire
the **generated Go API-v1 client**; contexts + auth (`login` / `whoami` / `context`,
config precedence, keychain-or-`0600` token storage); the `--json` contract and the
exit-code taxonomy (`0/1/2/3/4/5/6/7`); shell completion. Promote the `challenge.yaml`
parser/validator from the seeder into a **shared package** (`internal/challengespec` or
similar) that the seeder, the admin author-time path, and the CLI all import — a pure
refactor, no behaviour change — so `osctf challenge validate|package` gives results
identical to the server offline.

**Deliverables**: the `osctf` binary; the shared `challengespec` package; the CLI command
tree; config/auth; shell completion.

**Acceptance** (the `cli` job, [`03-testing-ci.md`](03-testing-ci.md)):
```
cd api && go build ./cmd/osctf && go test ./cmd/osctf/...
```
- Golden path against a running deployment with a token: `login → whoami → challenge
  validate → challenge create → instance start → submit → scoreboard`, asserting `--json`
  shapes and exit codes.
- Offline: `challenge validate` good/bad (exit `0`/`6`) with no server; `challenge package`
  → a deterministic tarball.
- Parity: the CLI validator and the seeder/API give identical verdicts on the same specs.
- Boundary: `osctf` imports no server-only internals beyond the generated client and the
  shared `challengespec` package; it holds no DB handle and no business logic.

## M1 — The MCP server

**Tasks**: `osctf mcp` (stdio) per [`02-mcp.md`](02-mcp.md); generate the tool surface from
the OpenAPI operations (input schemas from operation parameters/body); **scope-gate** tool
exposure by the token's scope; gate destructive tools behind a `confirm:true` argument;
pass problem+json through as tool errors; advertise the resolved scope in the server
`instructions`.

**Deliverables**: the `mcp` subcommand; the generated tool surface; the scope/confirm gates.

**Acceptance**:
```
cd api && go test ./cmd/osctf/... -run MCP
```
A minimal MCP client: `tools/list` is scope-gated (a `read` token cannot see admin tools);
`get_scoreboard` (read) returns standings; a destructive tool is refused without
`confirm:true` and echoes what it would affect; the secret-leak scan over tool results is
clean. This is v0.3.1 success criterion 1's mechanism; the full agent flow (spin up a
practice event + read the scoreboard through MCP only) is demonstrated in M2.

## M2 — CI, docs, release

**Tasks**: add the **`cli`** job to CI (golden path + offline parity + MCP harness); update
`CHANGELOG.md` (`v0.3.1`), `.env.example` / README / `AGENTS.md` as needed for the new
binary; document install (`go install ./cmd/osctf`, or the release artifact) and MCP client
registration (`command: osctf`, `args: [mcp]`). Confirm the full pipeline is green including
the unchanged v0.3 jobs. Tag `v0.3.1`.

**Acceptance**:
```
cd api && go build ./... && go test ./cmd/osctf/...
# success criterion 1, demonstrated end to end:
#   point an MCP client at `osctf mcp` with an admin token; have it set an event window,
#   make three challenges visible, and read the scoreboard — no DB, no shell.
# exit criterion: an operator runs the CLI golden path AND an agent runs the MCP flow,
#   both against a stock deployment, token-only, no cookie.
```
All v0.3.1 success criteria in [`00-overview.md`](00-overview.md) are met and the v0.1–v0.3
suites remain green. Then tag + release.

---

## Milestone → success-criterion map

| Criterion ([00](00-overview.md)) | Proven in |
|---|---|
| 1 — agent runs an event via MCP only | M1 (harness) + M2 (end-to-end demo) |
| 2 — CLI golden path | M0 (`cli` job golden path) |
| 3 — offline authoring, server-identical | M0 (offline + parity legs) |
| 4 — no privileged path (token-scoped) | M0 + M1 (boundary + scope-gating); inherits v0.3's token-only API guarantee |

## Notes for the building agent

- **v0.3 is a hard prerequisite.** If API v1 isn't mounted or tokens don't work, stop and
  finish v0.3 — this version has nothing to stand on otherwise.
- **The client adds no capability.** If a command or tool needs something the API can't do,
  that is an API endpoint to add in the API spec, not logic to add in the client. Record it
  and route it to the API surface.
- **Generated, not hand-maintained.** The CLI's HTTP client and the MCP tool schemas both
  come from the OpenAPI; keep them generated so they can't drift from the contract.
- **Secrets discipline is inherited.** The leak scan extends over CLI `--json` output and
  MCP tool results. No TODOs in code.
