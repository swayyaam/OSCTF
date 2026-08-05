# 11 — Plugin Template & Author Kit

The exit criterion is met when someone **outside the core team** ships a working plugin
without a PR against core. That requires three things to exist and be good: the **SDK** (so
a plugin is a few lines of `main`), the **template repo** (so the first commit already
builds and passes a contract test), and **author docs** (so the process is obvious to a
human *and* an agent). All three ship in v0.3.

## The SDK (`plugin/sdk`)

An importable Go package that hides go-plugin + gRPC boilerplate. An author implements one
small Go interface per plugin type and calls `sdk.Serve(...)`:

```go
package main

import "github.com/osctf/platform/plugin/sdk"

type engine struct{}
func (engine) Info() sdk.Info { return sdk.Info{Name: "linear-decay", Type: sdk.Scoring, ABI: "1.0", Version: "0.1.0"} }
func (engine) Value(in sdk.Score) int {
    v := in.Initial - in.Solves*atoiDefault(in.Params["step"], 50)
    if v < in.Min { return in.Min }
    return v
}

func main() { sdk.Serve(sdk.Scoring, engine{}) } // handshake, gRPC, lifecycle handled
```

The SDK provides: the handshake constants, the generated plugin-side stubs re-exported as
plain Go types (no raw protobuf), typed helpers for each service (`sdk.Auth`,
`sdk.Scoring`, `sdk.Notifier`, `sdk.ChallengeType`), config access
(`sdk.Config().String("issuer")`), structured logging that the host captures, and
`sdk.Serve` which wires everything and blocks. It is the **only** OSCTF package a plugin
imports (plus `pluginpb` transitively) — never `internal/*`.

## The template repo (`osctf-plugin-template`)

A standalone Git repo (in the monorepo during v0.3 as `templates/plugin/`, split out later
per the org roadmap) that clones into a buildable plugin:

```
osctf-plugin-template/
  go.mod                    # requires github.com/osctf/platform (for plugin/sdk only)
  main.go                   # a working SCORING plugin by default; comments show the other types
  plugin.yaml               # filled-in manifest with a config example
  Makefile                  # build, test, package
  plugin_test.go            # a contract test using plugin/plugintest against your binary
  AGENTS.md                 # setup + how to change the plugin type + how to test/package
  README.md                 # human quickstart
  .github/workflows/ci.yml  # build + contract test on push
```

In v0.3 you scaffold by **cloning or copying the template** (`git clone …` or copy
`templates/plugin/`) and editing the two marked lines to pick the type. From a clean clone:
`make test` runs the contract test against the built binary; `make package` produces the
plugin directory. (The `osctf init plugin <name> --type …` scaffolder is a
[v0.3.1](../v0.3.1/README.md) convenience that pre-selects the type for you — it saves the
copy-and-edit step, nothing more.)

## Packaging is a directory convention, not a tool (v0.3)

This is a fixed decision, called out because the **exit criterion depends on it**: in v0.3,
**packaging a plugin needs no CLI.** A "package" *is* a directory —

```
<plugin-name>/
  plugin.yaml     # the manifest: name, type, ABI version, executable, config schema
  <plugin-name>   # the built executable
```

— dropped into `OSCTF_PLUGINS_DIR`. The template's `make package` builds the binary and
lays out that directory (and, optionally, `tar`s it); it uses only Go + `make`, no OSCTF
binary. This keeps G10 self-sufficient in v0.3: a third party ships a plugin with the SDK,
the template, and `make` alone — before the CLI exists.

**Why not defer packaging to the CLI?** Because the exit criterion ("someone outside the
core team ships a working plugin") must hold in v0.3, and the CLI ships in
[v0.3.1](../v0.3.1/README.md). If packaging required `osctf plugin package`, the exit
criterion would silently depend on an unbuilt tool. So packaging is a documented layout the
platform reads directly; **v0.3.1's `osctf plugin package` automates the identical layout**
and adds no capability — it is convenience over a convention that already stands on its own.

## The contract-test harness (`plugin/plugintest`)

Shipped for authors, not just core: it boots your plugin **binary** through the real loader
and asserts ABI conformance and behaviour — the same harness the first-party plugins use
([`09-testing-ci.md`](09-testing-ci.md)). So an author's `make test` exercises exactly what
the platform will do in production, before they ever install it.

```go
func TestScoring(t *testing.T) {
    p := plugintest.Load(t, "./dist/linear-decay")   // launches the binary via go-plugin
    got := p.Scoring().Value(plugintest.Score{Initial: 500, Min: 100, Solves: 3, Params: map[string]string{"step": "50"}})
    if got != 350 { t.Fatalf("got %d", got) }
}
```

## Author docs (`docs/plugins/` + the template's AGENTS.md)

Human + agent readable, per the AI-native principle:

- **Quickstart:** clone → pick a type → implement → `make test` → `make package` → install.
- **The four types**, each with the SDK interface, a minimal example, the config schema
  rules, and the secrets rule (secret config is env-only; never log or return it).
- **Manifest reference** (`plugin.yaml` fields; ABI version meaning).
- **Lifecycle & failure model:** your plugin may be killed and restarted; be stateless or
  idempotent; keep calls fast (the host bounds them). Health = your `Info` responding.
- **Testing:** the `plugintest` harness; the boundary rule (no `internal/*` imports);
  determinism for scoring.
- **Publishing:** in v0.3 you ship a directory (`make package`; the convention above). The
  `osctf plugin package` convenience is [v0.3.1](../v0.3.1/README.md); the future registry is
  a post-1.0 concern.

The template's **`AGENTS.md`** makes the repo agent-ready from the first clone: setup steps,
"to change the plugin type, edit these two lines," the test/package commands, and the
boundary/secrets rules — so a user can open an agent and say "make this a Discord notifier"
and it has everything it needs.

## Definition of done for the author kit

1. Copy/clone the template, pick a type, and `make test` passes on a clean machine with only
   **Go + `make` + the OSCTF module** — no core checkout, no `osctf` binary.
2. `make package` output dropped into a running deployment's `OSCTF_PLUGINS_DIR` and a
   `serve` restart shows it **healthy in `GET /api/v1/admin/plugins`** and active in the
   platform.
3. The template's contract test, boundary check (no `internal/*`), and CI workflow are
   green out of the box.

## Decision log

- **The SDK is the only core dependency a plugin has, and it re-exports plain types.**
  Authors never touch raw protobuf or go-plugin wiring; the boundary stays clean and the
  ABI can evolve behind the SDK.
- **The template ships a passing contract test on first clone.** The fastest way to make
  "build a plugin" feel achievable to a human or an agent.
- **`plugintest` is public.** Authors test against the real loader/ABI, so "works on my
  machine" means "works in the platform."
- **AGENTS.md in the template.** Every OSCTF repo — including a plugin — is agent-ready from
  clone, per the AI-native principle.
