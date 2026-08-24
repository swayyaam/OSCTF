# AGENTS.md — building an OSCTF plugin from this template

Instructions for an agent (or a human) turning this template into a working plugin. The clone
already builds and passes its contract test; you extend it.

## Setup

- Go (matching `go.mod`'s version) is all you need. No platform checkout, no protobuf toolchain.
- `make build` compiles the plugin; `make test` runs the contract test; `make package` produces
  the loadable directory. `make tidy` resolves modules.
- You import exactly one OSCTF package: `github.com/swayyaam/OSCTF/plugin/sdk` (and, in the test,
  `.../plugin/sdk/contract`). Never import anything under `internal/`.

## The rule that keeps a plugin simple

The SDK owns the **type**, the **ABI**, and the **capabilities**; you own **identity** (`Info`)
and **behaviour** (the type's methods). Do not try to set type/ABI in code — you can't, and that
is the point: a plugin cannot misdeclare the contract it speaks.

## Change the plugin type

Each type is one small Go interface plus the matching `sdk.Serve` key and `plugin.yaml` `type`.
To switch, replace the interface `engine` implements, the `sdk.Serve(...)` call, `plugin.yaml`'s
`type`, and the contract verifier in `plugin_test.go`.

| Type | `plugin.yaml type` | Implement (`sdk.` interface) | `sdk.Serve(...)` | Contract verifier |
|---|---|---|---|---|
| Scoring (default) | `scoring` | `Scorer`: `Info() Info`, `Value(Score) int` | `Serve(sdk.Scoring, engine{})` | `contract.VerifyScoring` |
| Notification | `notification` | `Notifier`: `Info() Info`, `Subscriptions() []string`, `Notify(Event) error` | `Serve(sdk.Notification, engine{})` | `contract.VerifyNotification` |
| Challenge type | `challenge_type` | `Checker`: `Info() Info`, `ValidateConfig(map[string]string) ConfigValidation`, `CheckFlag(FlagCheck) (bool, error)` | `Serve(sdk.ChallengeType, engine{})` | *(see the SDK's contract package)* |
| Auth | `auth` | `PasswordAuth` and/or `RedirectAuth` (see the SDK docs — an `Identity` is a **claim, not a grant**) | `Serve(sdk.Auth, engine{})` | *(see the SDK's contract package)* |

Notes:

- **Notification** is fire-and-forget and best-effort: return quickly and do slow work (HTTP
  posts, etc.) on your own goroutine — the host does not block a solve on you, and a full queue
  drops the newest event (counted, never silent). `Subscriptions()` returns the event names you
  want (`"*"` = all).
- **Auth** cannot be loaded by the host yet (its registrar is wired in a later milestone); you can
  still develop and contract-test it. Read the `Identity` doc in the SDK: it is informational, not
  authority — the host maps identity to a user under its own policy.

## Config, logging, and event data

**Config** — declare typed keys in `plugin.yaml` under `config:` and read them with `sdk.Config()`:
`sdk.Config().String("webhook_url")`, `.Int("step")`, `.Bool("enabled")`. Precedence and safety are
the host's job: a `secret: true` key resolves from the **host environment only**
(`OSCTF_PLUGIN_<NAME>_<KEY>`), never from the committed manifest; an env value overrides a manifest
default; and the host **validates config against the manifest before your plugin starts** — a
missing required or wrong-typed key quarantines the plugin at load with a clear reason (visible in
the admin plugin view), not a plugin that starts and then fails every call. `main.go` shows the
call, commented.

**Logging** — `sdk.Log().Debug/Info/Warn/Error(msg, key, value, …)`. Output reaches the operator's
host logs, tagged as your plugin. It is rate-limited and truncated by the host (a chatty or
crash-looping plugin cannot flood it), so do not rely on every line being kept. **Never log a
secret, a config value, an event payload, or a flag** — it lands in the operator's logs. `main.go`
shows the call, commented.

**Event data** (notification plugins) — an `sdk.Event`'s `Data` keys per event type are documented
by `sdk.EventKeys(name)` (e.g. `sdk.EventKeys("challenge.solved")`). These are the same keys the
host emits, so they won't drift from what your `Notify` receives.

## Test and package

- `make test` builds the plugin and dials it through the public contract harness — the same dial
  the host uses. Update the cases in `plugin_test.go` to match your logic, and use the verifier
  for your type.
- `make package` writes `dist/<name>/` (binary + `plugin.yaml`). Copy that into the host's
  plugins directory and restart the host. Cross-compile for the host (`GOOS`/`GOARCH`) if needed.

## Local development against an unpublished SDK

Only if you are changing the SDK itself alongside the plugin: `go mod edit -replace
github.com/swayyaam/OSCTF=/path/to/platform`. **Do not commit that replace** — the shipped build
must resolve the SDK via `go get`, which is what every other author has.
