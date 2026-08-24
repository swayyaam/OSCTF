# OSCTF webhook plugin

A reference [OSCTF](https://github.com/swayyaam/OSCTF) **notification** plugin: it POSTs every
subscribed event to an HTTP endpoint as JSON. It's a small, complete worked example of a plugin
that needs **configuration** and **logging** — the non-trivial case — built entirely against the
public [`plugin/sdk`](https://pkg.go.dev/github.com/swayyaam/OSCTF/plugin/sdk), with no OSCTF
source checkout and no `replace` directive. Made from the
[plugin template](https://github.com/osctf/plugin-template).

## What it does

On each event it subscribes to, it POSTs a JSON body to your configured URL:

```json
{
  "event": "challenge.solved",
  "id": "0192…",
  "occurred_at": "2026-08-16T10:00:00Z",
  "data": { "team_id": "…", "user_id": "…", "challenge_id": "…", "challenge_slug": "sanity" }
}
```

It subscribes to **all** events (`"*"`); the `data` fields per event type are the ones OSCTF
documents via `sdk.EventKeys(name)`. Delivery is **fire-and-forget**: `Notify` returns immediately
and the POST runs on its own goroutine, so a slow endpoint never delays a solve, and a failed
delivery is reported through `sdk.Log()` (captured in the host's logs) rather than by blocking. If
no URL is configured, events are accepted and dropped.

## Configure it

One config key, **`webhook_url`** — declared in [`plugin.yaml`](plugin.yaml) as **`secret: true`**,
because the URL may embed a token. A secret resolves from the **host environment only** (never from
a committed manifest). Set it on the host as `OSCTF_PLUGIN_WEBHOOK_WEBHOOK_URL`
(`OSCTF_PLUGIN_<NAME>_<KEY>`, upper-cased):

```bash
export OSCTF_PLUGIN_WEBHOOK_WEBHOOK_URL="https://hooks.example.com/services/T000/B000/xxxx"
```

The host validates config against the manifest **before the plugin starts** — a missing required
key quarantines the plugin at load with a clear reason in the admin plugin view, rather than a
plugin that starts and then fails every delivery.

## Build, test, install

```bash
make build     # -> ./webhook
make test      # builds + runs the contract test against the binary (no OSCTF checkout needed)
make package   # -> dist/webhook/  (the binary + plugin.yaml)
```

Copy `dist/webhook/` into the host's plugins directory (`OSCTF_PLUGINS_DIR`, default `./plugins`)
so the layout is `…/plugins/webhook/{webhook, plugin.yaml}`, set `OSCTF_PLUGIN_WEBHOOK_WEBHOOK_URL`,
and restart the host. Build for the host's OS/arch (`GOOS=linux GOARCH=amd64 make build`) if you
develop on a different platform. Mount the plugins directory read-only.

## ABI

Targets OSCTF plugin ABI **`1.0`**. The major (`1`) must match the host — a mismatch is refused at
the handshake — and the minor is forward-compatible. The ABI travels with the `plugin/sdk` version
in [`go.mod`](go.mod); bump that require and rebuild to move to a newer ABI.
