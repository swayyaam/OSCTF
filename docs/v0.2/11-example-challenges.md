# 11 — Example Challenges (`challenge.yaml` additions)

v0.2 adds a few fields to the `challenge.yaml` format and ships new examples that exercise
per-team instancing, per-instance flags, and the hardening defaults. The v0.1 examples are
untouched and keep working (they are all `shared`/`static`). Baseline:
[`../v0.1/13-example-challenges.md`](../v0.1/13-example-challenges.md) and
`api/internal/seed/challenge_yaml.go`.

## `challenge.yaml` — new fields (all optional, v0.1-safe defaults)

Add to the seed struct (`challenge_yaml.go`) and validator:

```yaml
instancing: per_team        # shared (default) | per_team — container only
flag_mode: per_instance     # static (default) | per_instance — container only
instance_ttl_seconds: 1800  # optional; null/omitted = OSCTF_INSTANCE_TTL; 0 = no TTL
egress: false               # default true; false → per-team network is --internal
writable_paths:             # extra tmpfs mounts on top of /tmp (read-only rootfs default)
  - /var/run
```

Validation (mirrors the DB/API constraint [`02-database.md`](02-database.md)):

- `instancing`/`flag_mode` other than the `shared`/`static` defaults, and any
  `instance_ttl_seconds`/`egress:false`/`writable_paths`, require `kind: container`.
- `flag_mode: per_instance` requires the image to actually read `FLAG` at runtime (the
  platform injects the per-instance flag there — the existing `inject_flag` behaviour, now
  the flag value is per instance).
- Defaults keep every v0.1 `challenge.yaml` valid and identical in behaviour.

## Interaction with `flag` for `per_instance`

`flag:` stays **required** in the YAML (the schema/DB contract), but for
`flag_mode: per_instance` it is a **fallback/template only** — the effective flag is the
per-instance one the scheduler generates and injects. Author it as a clearly-non-winning
placeholder, e.g. `flag: "OSCTF{per_instance_dynamic}"`, so a misconfiguration (mode not
honored) is obvious rather than silently accepting a static flag. The example's service must
serve `$FLAG` from the environment, never a baked-in string.

## New examples to ship (≥ 2 per-team + a hardening demo)

Build under `examples/challenges/`, images `osctf/example-<slug>:0.2`, wired into
`scripts/build-examples.sh`.

### 1. `per-team-web` — web, `per_team` + `per_instance`

A small web app where the flag lives at an authenticated route; each team gets its own
container and its own flag, so a leaked URL/flag from another team doesn't solve it.

```yaml
slug: per-team-web
title: Your Very Own Web
category: web
difficulty: easy
description: |
  This challenge gives **your team its own instance**. Click Start, wait for the
  connection info, and go find the flag your instance is serving. The flag is
  unique to your team — someone else's flag won't score for you.
flag: "OSCTF{per_instance_dynamic_placeholder}"
scoring: dynamic
points_initial: 200
points_min: 80
decay: 25
visible: true
kind: container
image: osctf/example-per-team-web:0.2
internal_port: 8000
connection_template: "http://{host}:{port}"
instancing: per_team
flag_mode: per_instance
instance_ttl_seconds: 3600
egress: false
```

The app reads `os.Getenv("FLAG")` and serves it behind a trivial gate; it writes only to
`/tmp` (read-only rootfs holds).

### 2. `per-team-pwn` — pwn, `per_team` + `per_instance`

A classic `nc` service, one per team, dynamic flag — proves the pwn path (the exit
criterion names pwn/web).

```yaml
slug: per-team-pwn
title: Your Very Own Shell
category: pwn
difficulty: medium
description: |
  A network service, **one instance per team**, that reads your input and — if
  you win the memory game — prints your team's unique flag. Start your instance,
  connect with `nc`, and exploit it. Your flag is yours alone.
flag: "OSCTF{per_instance_dynamic_placeholder}"
scoring: dynamic
points_initial: 400
points_min: 150
decay: 20
visible: true
kind: container
image: osctf/example-per-team-pwn:0.2
internal_port: 9000
connection_template: "nc {host} {port}"
instancing: per_team
flag_mode: per_instance
instance_ttl_seconds: 3600
egress: false
mem_limit_mb: 128
files:
  - files/vuln.c
```

Prints `getenv("FLAG")` on success; source attached as `vuln.c`. Runs read-only-rootfs; if
it needs a writable scratch path, declare it via `writable_paths`.

### 3. `hardening-demo` — misc, `shared`, showcases the hardening pass

A deliberately introspective challenge: the flag is reachable, but the container is
read-only-rootfs with `/tmp` tmpfs and (optionally) no egress — participants (and
organizers) can see the hardening in effect.

```yaml
slug: hardening-demo
title: Locked Down
category: misc
difficulty: easy
description: |
  This service runs read-only: its root filesystem is immutable, only `/tmp` is
  writable (and noexec), all Linux capabilities are dropped, and it can't call
  home. The flag is in an environment variable — try to make the container do
  something it shouldn't, and watch it fail.
flag: "OSCTF{read_only_rootfs_and_dropped_caps}"
scoring: static
points_initial: 100
visible: true
kind: container
image: osctf/example-hardening-demo:0.2
internal_port: 8080
connection_template: "http://{host}:{port}"
egress: false
writable_paths:
  - /data
```

Stays `shared`/`static` (so it also proves hardening applies to shared instances, not just
per-team). Attempts to write outside `/tmp`/`/data` fail; the demo surfaces that.

## Build & seed

- `scripts/build-examples.sh` builds the three new images at tag `:0.2` alongside the v0.1
  `:0.1` images. The seed's `image:` references match.
- The seed parser reads the new fields, validates them, and writes the new `challenges`
  columns. Seeding stays **idempotent** (upsert by slug), as in v0.1 — re-seeding an
  upgraded DB updates fields without duplicating rows.
- `OSCTF_SEED_EXAMPLES=true` (default) loads all examples, v0.1 + v0.2.

## Authoring guidance (README/AGENTS note)

Add a short "per-team & dynamic flags" section to the challenge-authoring docs:

- Use `per_team` when a challenge is stateful/exploitable and teams would interfere with
  each other on a shared instance (pwn, most web).
- Use `flag_mode: per_instance` to make flag-sharing detectable and to stop a leaked flag
  from scoring for the leaker's rivals; the service must serve `$FLAG`.
- Default to `egress: false` for untrusted challenge code; opt into egress only when the
  task genuinely needs the network.
- Read-only rootfs is the default — write to `/tmp` or declare `writable_paths`; don't rely
  on a writable root.

## Decision log

- **`flag` stays required even for `per_instance`.** Keeps the YAML/DB contract stable; a
  visible placeholder makes a mode-misconfiguration obvious.
- **Ship a `shared` hardening demo.** Proves the hardening pass applies universally and gives
  organizers a visible reference for the new isolation defaults.
- **Images tagged `:0.2`.** Coexist with v0.1 `:0.1` images; no retag churn on the existing
  examples.
