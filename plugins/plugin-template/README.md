# OSCTF plugin template

A starting point for an [OSCTF](https://github.com/swayyaam/OSCTF) plugin. Click **“Use this
template”** to make your own repo, then edit two or three files. The clone already **builds and
passes a contract test** — you extend a working plugin, you don't assemble one.

A plugin is a small separate program the OSCTF host runs as a child process and talks to over
gRPC. You never write any of that: you implement one small Go interface and call `sdk.Serve`.
The only OSCTF package you import is [`github.com/swayyaam/OSCTF/plugin/sdk`](https://pkg.go.dev/github.com/swayyaam/OSCTF/plugin/sdk) —
never anything under `internal/`.

As shipped this is a **scoring** plugin (a linear-decay curve). It can also be a notification,
challenge-type, or auth plugin — see [Change the plugin type](#change-the-plugin-type).

## What to change

| File | Change |
|---|---|
| [`main.go`](main.go) | Your logic — `Info()` (name + version) and `Value()` (the scoring curve). |
| [`plugin.yaml`](plugin.yaml) | `name`, `type`, `description` to match your plugin. Add `config:` keys if you need them. |
| [`plugin_test.go`](plugin_test.go) | The contract-test cases, to match your `Value()`. |
| `go.mod` (module line) | Rename `github.com/swayyaam/OSCTF/plugins/plugin-template` to your module path. |

## What NOT to touch

- **You never import `go-plugin`, gRPC, or protobuf.** The SDK owns the handshake and the wire.
- **You do not set the plugin type or the ABI in `Info()`.** The SDK stamps the type (from the
  `sdk.Serve` argument) and the ABI (from the SDK version you build against), so they cannot be
  misdeclared. `Info` is just `{Name, Version}`.
- **Don't add a `replace` directive to ship.** The `require` on `github.com/swayyaam/OSCTF` is
  what makes this build for anyone. (A `replace` is fine *locally* while developing against an
  unpublished SDK checkout — see [AGENTS.md](AGENTS.md) — but it must not be committed, or the
  build only works on your machine.)

## Config and logging

If your plugin needs **configuration**, declare typed keys in [`plugin.yaml`](plugin.yaml) under
`config:` and read them with `sdk.Config().String("…")` / `.Int` / `.Bool`. A `secret: true` key
resolves from the host environment only (`OSCTF_PLUGIN_<NAME>_<KEY>`); the host validates config
before your plugin starts, so a bad config quarantines it at load rather than failing at first call.

To **log**, use `sdk.Log().Debug/Info/Warn/Error` — output reaches the host's logs, tagged as your
plugin (rate-limited + truncated by the host). **Never log a secret, config value, event payload,
or flag.** See [`main.go`](main.go) (commented) and [AGENTS.md](AGENTS.md) for both.

A **challenge-type** plugin also receives a per-challenge `type_config` map (in `ValidateConfig` and
`CheckFlag`). It is author-defined and **may contain challenge-sensitive data** — a regex that
reveals the flag's structure is the obvious case — so **never log `type_config` or anything derived
from it**: a flag hint in the host log defeats the challenge.

## Build

```bash
make build       # -> ./my-plugin
```

## Test

```bash
make test        # builds the plugin and runs the contract test against the binary
```

The contract test ([`plugin_test.go`](plugin_test.go)) dials your built plugin exactly as the
host does, using the public `plugin/sdk/contract` harness, and checks it satisfies the ABI. It
answers **“is my plugin correct?” without needing the platform source.**

## Install into a running deployment

```bash
make package     # -> dist/my-plugin/  (the binary + plugin.yaml)
```

Copy that directory into the host's plugins directory (`OSCTF_PLUGINS_DIR`, default `./plugins`)
so the layout is `…/plugins/my-plugin/{my-plugin, plugin.yaml}`, then restart the host. It
discovers the plugin at boot, launches it, and registers it once it reports ready; a malformed or
crashing plugin fails alone and never takes the host down. **Mount the plugins directory
read-only** — the host warns at boot if it is writable (a writable plugins dir is a persistence
path for a compromised core).

Build the binary for the host's OS/arch (e.g. `GOOS=linux GOARCH=amd64 make build`) if you
develop on a different platform.

## ABI version

This template targets **OSCTF plugin ABI `1.0`** (`plugin.yaml`'s `abi`). The **major** (`1`)
must match the host — a mismatch is refused at the handshake, before any call — and the **minor**
is forward-compatible. You don't manage the ABI in code: it travels with the `plugin/sdk` version
your `go.mod` requires. To move to a newer ABI, bump that require and rebuild.
