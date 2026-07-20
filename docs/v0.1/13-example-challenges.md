# 13 — Example Challenges & `challenge.yaml`

The seeded challenges do double duty: they make a fresh install non-empty, and they are the reference for the authoring format. Every one must be **solvable** with the intended technique (M10 acceptance requires solving each).

## `challenge.yaml` format

One file per challenge at `examples/challenges/<slug>/challenge.yaml`. This is also the format documented in `docs/guides/authoring.md`. Parser lives in `seed` (v0.1); it becomes the CLI's `validate`/`package` input in Phase 3.

```yaml
# examples/challenges/<slug>/challenge.yaml
slug: cookie-monster            # ^[a-z0-9]+(-[a-z0-9]+)*$ ; unique ; = directory name
title: Cookie Monster
category: web                   # web|pwn|crypto|rev|forensics|misc
difficulty: easy                # intro|easy|medium|hard|insane (optional)
description: |                  # markdown
  The admin's cookie is too tasty. Become the admin.
flag: "OSCTF{c00kies_r_stateful}"
flag_case_insensitive: false    # optional, default false

scoring: dynamic                # static|dynamic (default dynamic)
points_initial: 300
points_min: 100                 # dynamic only
decay: 40                       # dynamic only

max_attempts: null              # optional; null/omitted = unlimited
visible: true                   # seed as visible

# --- container kind only ---
kind: container                 # standard|container (default standard)
image: osctf/example-cookie-monster:0.1
internal_port: 5000
mem_limit_mb: 128               # optional; defaults per 04-database.md
cpu_millis: 500                 # optional
connection_template: "http://{host}:{port}"   # optional; sensible default per kind
inject_flag: true               # optional, default true; injects FLAG env into container
container_env:                  # optional extra env for the image
  DIFFICULTY: normal

# --- attachments (both kinds) ---
files:                          # optional; paths relative to this dir, uploaded as attachments
  - files/capture.pcap
```

Rules:
- `slug` must equal the directory name.
- `standard` kind: `image`/port fields forbidden; `files` typical.
- `container` kind: `image` + `internal_port` required; the image must be built by `make examples` before deploy (v0.1 doesn't push to a registry).
- Validation mirrors the DB CHECK constraints in [`04-database.md`](04-database.md); the parser rejects with a precise message (used by the future CLI `validate`).
- Container images **must not contain the flag** unless `inject_flag: false`; the platform injects `FLAG`. The image reads `os.Getenv("FLAG")`.

## Build layout for container examples

```
examples/challenges/<slug>/
├── challenge.yaml
├── files/                 # attachments handed to players (source, pcaps, binaries)
└── src/                   # image build context (Dockerfile + app) — container kind only
    ├── Dockerfile
    └── app…
```

`make examples` = `for each container challenge: docker build -t <image> src/`. Images use the `osctf/example-<slug>:0.1` tag matching `challenge.yaml`.

## The eight seeded challenges

Coverage: 6 categories, mix of `standard`/`container`, mix of `static`/`dynamic`, one intentional intro freebie, one that exercises attachments, four that exercise the container runtime.

| # | slug | cat | kind | scoring | teaches (platform surface) |
|---|---|---|---|---|---|
| 1 | `sanity-check` | misc | standard | static 50 | submission happy path; known freebie flag `OSCTF{welcome_to_the_game}` (used by smoke test) |
| 2 | `base-what` | crypto | standard | dynamic | attachment download; pure-thought solve |
| 3 | `hidden-in-plain-sight` | forensics | standard | dynamic | binary attachment (image with EXIF/strings) |
| 4 | `robots-rule` | web | container | dynamic | container deploy; HTTP connection info |
| 5 | `cookie-monster` | web | container | dynamic | container; session/cookie tampering |
| 6 | `env-hunter` | misc | container | static | proves flag **injection** (flag only in `FLAG` env, not the image) |
| 7 | `overflow-lite` | pwn | container | dynamic | `nc` connection template; a gentle buffer overflow |
| 8 | `xor-me` | rev | standard | dynamic | attached small binary; static reversing |

### Per-challenge specs

**1 — sanity-check** (`misc`, standard, static 50, visible)
- Description: "Flags look like `OSCTF{...}`. Submit this one to warm up: `OSCTF{welcome_to_the_game}`."
- Flag: `OSCTF{welcome_to_the_game}`. This exact flag is referenced by `scripts/smoke.sh` — do not change it without updating the smoke test.

**2 — base-what** (`crypto`, standard, dynamic 200/100/30)
- Description gives a base64 blob that decodes to a base32 blob that decodes to the flag. Pure client-side.
- Flag: `OSCTF{layers_of_base_are_not_crypto}`. No attachment (blob is inline in the markdown), but include a `files/encoded.txt` copy for convenience — exercises the attachment path.

**3 — hidden-in-plain-sight** (`forensics`, standard, dynamic 250/100/30)
- `files/badge.png` with the flag in EXIF `UserComment` (and as a `strings`-visible trailer). Build the asset in `src/make_asset.py` run by `make examples` (deterministic output — no timestamps).
- Flag: `OSCTF{metadata_tells_all}`.

**4 — robots-rule** (`web`, container, dynamic 200/100/40)
- Tiny Flask/Go app: `/robots.txt` disallows `/s3cr3t-admin`, which serves the flag. `internal_port` 8000, `connection_template: "http://{host}:{port}"`.
- Flag injected via `FLAG` env, rendered at the secret path.

**5 — cookie-monster** (`web`, container, dynamic 300/100/40)
- App sets `role=guest` cookie; setting `role=admin` reveals the flag. Demonstrates trivial cookie tampering. `internal_port` per image.
- Flag injected.

**6 — env-hunter** (`misc`, container, static 100)
- App exposes an endpoint listing *most* env vars but the flag is only reachable by reasoning about the app (e.g. `/debug?var=FLAG`). The point is to prove the platform injects `FLAG` and images ship without the flag baked in — verify by `docker history`/`strings` on the image showing no flag. `inject_flag: true`.
- Flag injected; not present in the image layers.

**7 — overflow-lite** (`pwn`, container, dynamic 350/150/25)
- A small C binary served over TCP via `socat`/inetd style; a stack buffer overflow overwrites a `won` flag variable that gates printing `FLAG`. Compiled `-fno-stack-protector -no-pie` for approachability. `connection_template: "nc {host} {port}"`, `internal_port` matches the listener.
- Ship the source in `files/vuln.c` as an attachment (players need it). Flag injected via env, printed on success.

**8 — xor-me** (`rev`, standard, dynamic 300/100/30)
- `files/checker` (small ELF, x86-64) that XORs input with a fixed key and compares; reversing the key yields the flag. Built reproducibly by `make examples` from `src/checker.c`.
- Flag: `OSCTF{x0r_is_reversible_obviously}` (also the string the binary checks, XORed).

### Authoring-guide callouts (documented, demonstrated by these examples)

- **Shared instances**: challenges 4–7 are single shared containers — all teams hit the same instance. Authoring guide warns: no per-team secrets, no destructive shared state; `cookie-monster`/`robots-rule` are stateless by design as the reference pattern.
- **Flag injection**: `env-hunter` is the canonical "don't bake the flag in" example; the guide points at it.
- **Isolation limits**: `overflow-lite` is deliberately gentle; the guide states plainly that shared Docker isolation is unsuitable for kernel-level or destructive pwn and that per-team instances (v0.2) are the answer.

## Solvability checklist (M10 gate)

A release blocker until every line is checked, each solved on a clean `docker compose up`:

- [ ] 1 submit known flag → correct
- [ ] 2 decode chain → flag
- [ ] 3 `exiftool`/`strings` on the PNG → flag
- [ ] 4 deploy, read robots.txt, hit secret path → flag
- [ ] 5 deploy, tamper cookie → flag
- [ ] 6 deploy, reach the flag env, and confirm `strings`/`docker history` on the image shows no flag
- [ ] 7 deploy, `nc`, overflow → flag
- [ ] 8 reverse `checker` → flag
