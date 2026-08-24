# Reference plugins

One plugin per extensible type, plus the template an author starts from. They exist to prove
each interface is usable from outside the platform — if a reference plugin needed something
from `internal/`, the ABI would be missing something and *that* would get fixed.

| Directory | Type | Proves |
|---|---|---|
| `plugin-template/` | scoring | the starting point an author copies |
| `first-blood-plugin/` | scoring | a policy the built-in engines do not offer (a permanent first-blood bonus) |
| `webhook-plugin/` | notification | events reaching an external sink |
| `regex-flag-plugin/` | challenge_type | per-challenge config deciding correctness |
| `oidc-plugin/` | auth | OIDC/OAuth2 login (authorization code + PKCE) |

## These are sources, not a load directory

Each is its **own Go module** and is not part of the platform module — `go build ./...` at the
repo root does not see them. They carry a `plugin.yaml`, but their executables are build
outputs, so **this directory is not something to point `OSCTF_PLUGINS_DIR` at**: the loader
would discover five plugins and quarantine every one for a missing executable. Build and
package a plugin (`make package`) and drop the result into your deployment's plugins directory.

## Why they are here at all

They were, and will be again, separate repositories — an in-tree plugin can reach core sources
and lean on a `replace`, so it would build even when the published SDK is incomplete, which is
the exact failure the exit criterion exists to catch. They are vendored here only until the
organisation that will host them exists.

That property is preserved by construction rather than by geography: each is a separate module
depending on a **published** platform version with no `replace`, there is no `go.work`, and the
exit gate is still run from an out-of-tree copy. `plugin.TestVendoredPluginsDoNotShortCircuitTheExitGate`
fails the build if either of those slips. See the Decision log in
[`../docs/v0.3/05-first-party-plugins.md`](../docs/v0.3/05-first-party-plugins.md).

## Working on one

```bash
cd plugins/<name>
make build      # compile
make test       # build + run its contract test through the real loader
make package    # dist/<name>/ with the binary + manifest, ready to drop in
```

They depend on the platform through a published version. Do **not** add a `replace` to make a
local change visible — that is precisely what the gate forbids. Push the platform change, then
`go get github.com/swayyaam/OSCTF@<commit-or-tag>`.
