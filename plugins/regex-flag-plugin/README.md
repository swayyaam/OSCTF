# OSCTF regex-flag challenge-type plugin

A reference [OSCTF](https://github.com/swayyaam/OSCTF) **challenge-type** plugin: it decides whether
a submitted flag is correct by matching it against a **per-challenge regular expression**. Built
through the [plugin template](https://github.com/osctf/plugin-template) against the public
[`plugin/sdk`](https://pkg.go.dev/github.com/swayyaam/OSCTF/plugin/sdk), with no OSCTF source
checkout and no `replace` directive.

A challenge-type plugin is the kind that **decides correctness** — the host calls its `CheckFlag`
instead of the built-in flag comparison. This one is the worked example of two things an author
gets wrong: **per-challenge config**, and **the attempt-consuming distinction**.

## What it computes

Each challenge configures a `pattern` (an [RE2](https://github.com/google/re2/wiki/Syntax) regular
expression) in its **`type_config`**. A submission is correct iff it matches:

```
type_config: { "pattern": "^OSCTF\\{[a-z0-9_]+\\}$" }

  OSCTF{sql_injection}   → correct
  OSCTF{WRONG}           → incorrect   (decided false — a wrong answer, costs an attempt)
  not-a-flag             → incorrect
```

`regexp` matches a **substring** by default — anchor with `^…$` if you mean the whole string, or
`OSCTF\{.+\}` will accept `junk OSCTF{x} junk`. The plugin stores your pattern verbatim; anchoring
is your call.

## Per-challenge, not per-deployment

The pattern lives in the **challenge's `type_config`**, not in the plugin's `sdk.Config`. That's the
difference between a challenge *type* and a single hardcoded challenge: one instance of this plugin
serves every regex challenge in the event, **each with its own pattern**. There is no `config:`
block in [`plugin.yaml`](plugin.yaml) — `config:` is for per-deployment values, and this plugin has
none.

The author sets `type_config` when creating the challenge; the host runs it through
`ValidateConfig` **at author time**, so a bad pattern is rejected on save with a per-field error the
admin sees — the challenge never goes live broken, and no player meets it mid-event only to find
every submission failing.

## The attempt-consuming distinction (read this before writing any checker)

`CheckFlag` returns `(correct bool, err error)`. Those are **three** outcomes, and conflating two of
them costs players their limited attempts:

| Return | Meaning | Host behaviour |
|---|---|---|
| `(true, nil)` | correct flag | solve recorded |
| `(false, nil)` | **decided** wrong answer | attempt consumed |
| `(_, err)` | the checker **cannot decide** | **fails closed, attempt NOT consumed**, player told to retry |

So an error is how a checker says *"the problem is mine, not the player's."* If the stored pattern
is missing or won't compile, this plugin returns an **error, never `false`** — returning `false`
would silently burn a try for a fault the player didn't cause. A submission that simply doesn't
match is a real wrong answer: that's a decided `false`, and consuming an attempt for it is correct.

Getting this backwards is the single most consequential mistake a checker author can make, which is
why the contract test asserts it ([`plugin_test.go`](plugin_test.go), `VerifyChallengeType` plus a
direct fail-closed check).

## Build, test, install

```bash
make build     # -> ./regex-flag
make test      # builds + runs the contract test (VerifyChallengeType) against the binary
make package   # -> dist/regex-flag/  (the binary + plugin.yaml)
```

Copy `dist/regex-flag/` into the host's plugins directory (`OSCTF_PLUGINS_DIR`, default `./plugins`)
and restart the host. Build for the host's OS/arch (`GOOS=linux GOARCH=amd64 make build`) if you
develop on a different platform. Mount the plugins directory read-only.

## ABI

Targets OSCTF plugin ABI **`1.0`** — the major (`1`) must match the host; the minor is
forward-compatible. The ABI travels with the `plugin/sdk` version in [`go.mod`](go.mod).
