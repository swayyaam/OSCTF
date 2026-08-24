# OSCTF first-blood scoring plugin

A reference [OSCTF](https://github.com/swayyaam/OSCTF) **scoring** plugin: the **first solver** of a
challenge locks in a bonus on top of the initial value; everyone after gets a linear decay by solve
count. Built through the [plugin template](https://github.com/osctf/plugin-template) against the
public [`plugin/sdk`](https://pkg.go.dev/github.com/swayyaam/OSCTF/plugin/sdk), with no OSCTF source
checkout and no `replace` directive.

It's the **pure** case: the whole scoring policy is a function of the `Score` the host passes —
**no config, no logging, no I/O**. (Contrast the [webhook plugin](https://github.com/osctf/webhook-plugin),
which needs both.) A scoring plugin is `Value(sdk.Score) int`, and that's the entire surface.

## What it computes

```
first solver (Solves == 1):  Initial + 50% of Initial     ← the first-blood bonus, locked at this solve
every solver after:          max(Min, Initial − (Solves−1)·Decay)
```

`Solves` includes the current solve, so the first solver sees `1`.

## Why a first-blood bonus (not linear decay)

The built-in **`dynamic`** engine already does solve-count decay (CTFd-style), so a decay plugin
would only reimplement a built-in and teach nothing about *why* you'd write one. A first-blood bonus
is a policy the core deliberately doesn't offer — and it's meaningful precisely because scoring is
**locked at solve**: `Value` runs once per solve and the result is recorded, so the first solver's
bonus is **permanent** — later solves never erode it. That's the design fact worth internalizing
before writing any scoring curve (see `sdk.Scorer`).

## Configure it

Nothing to configure — a pure scoring policy has no config block in [`plugin.yaml`](plugin.yaml).
The bonus is a fixed percent of the challenge's initial value (scaled per challenge via `Initial`).
A policy that needed a tunable would read it from `sdk.Config()` (per-plugin); note there is **no
per-challenge** scoring config — a scoring plugin's inputs are exactly `Initial/Min/Decay/Solves`.

## Build, test, install

```bash
make build     # -> ./first-blood
make test      # builds + runs the contract test (no OSCTF checkout needed)
make package   # -> dist/first-blood/  (the binary + plugin.yaml)
```

Copy `dist/first-blood/` into the host's plugins directory (`OSCTF_PLUGINS_DIR`, default `./plugins`)
and restart the host. Build for the host's OS/arch (`GOOS=linux GOARCH=amd64 make build`) if you
develop on a different platform. Mount the plugins directory read-only.

## ABI

Targets OSCTF plugin ABI **`1.0`** — the major (`1`) must match the host; the minor is
forward-compatible. The ABI travels with the `plugin/sdk` version in [`go.mod`](go.mod).
