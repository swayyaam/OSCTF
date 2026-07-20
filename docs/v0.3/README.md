# v0.3 — Extensibility

> Status: **planned stub — not yet build-ready.** Scope below is inherited from the roadmap in [`../project-desc.md`](../project-desc.md#L184). Do not start building from this file; first expand it into full topic docs following the [`../v0.1/`](../v0.1/README.md) template.

## Theme

**Nothing new requires touching core.** The plugin-first principle becomes real.

## Scope (from roadmap)

- **Plugin loader + lifecycle** (discover, load, configure, isolate failures) over the interfaces defined since v0.1 — `AuthProvider`, `ScoringEngine`, `ChallengeRuntime`, `ObjectStore`.
- **First-party plugins that prove each interface**: OAuth/SSO auth, an alternative scoring algorithm, Discord/webhook notifications, one custom challenge type.
- The **`platform` CLI**: `init`, `create challenge`, `validate`, `deploy`, `package` — structured output (`--json`), non-interactive flags, meaningful exit codes.
- **Public API v1 declared stable**: versioned (`/api/v1`), documented, semver-governed from here on. (v0.1 pins everything at `/api/v0` precisely so this is a clean cut, not a break.)
- **MCP server** over API v1 so agents can manage events and author challenges conversationally.
- Plugin author docs + a plugin template repo (with its own `AGENTS.md`).

## Exit criterion

Someone **outside the core team builds and ships a working plugin** without opening a PR against core.

## Builds on v0.1

- [`../v0.1/01-architecture.md`](../v0.1/01-architecture.md) — the four core interfaces are defined here with exactly one implementation each; this version adds the loader that lets others register additional implementations. The "plugin story via gRPC/HashiCorp go-plugin or WASM" decision from the vision doc is made here.
- [`../v0.1/05-api.md`](../v0.1/05-api.md) — API is `/api/v0` (explicitly "unstable until Phase 3"). Cutting `/api/v1` and its stability contract is a headline task of this version.
- [`../v0.1/13-example-challenges.md`](../v0.1/13-example-challenges.md) — the `challenge.yaml` parser lives in the v0.1 seeder; this version promotes it to the CLI's `validate`/`package` input.

## To make this build-ready

Write the numbered topic docs: the plugin ABI/loader design and isolation model, the plugin manifest format, the API v1 stability policy and diff from v0, the CLI command reference, the MCP server tool surface, the first-party plugin specs, and a milestone plan with acceptance checks — same depth as `../v0.1/`.
