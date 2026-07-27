# Authoring Challenges

A challenge is a directory under `examples/challenges/<slug>/` containing a
`challenge.yaml`. The same format is loaded by the first-boot seeder and (from
Phase 3) the CLI. The eight bundled examples are the reference — copy the closest
one.

## `challenge.yaml` reference

```yaml
slug: cookie-monster            # ^[a-z0-9]+(-[a-z0-9]+)*$ ; MUST equal the directory name
title: Cookie Monster
category: web                   # web | pwn | crypto | rev | forensics | misc
difficulty: easy                # intro | easy | medium | hard | insane (optional)
description: |                  # markdown, shown to players
  The admin's cookie is too tasty. Become the admin.
flag: "OSCTF{c00kies_r_stateful}"
flag_case_insensitive: false    # optional, default false

scoring: dynamic                # static | dynamic (default dynamic)
points_initial: 300
points_min: 100                 # dynamic only
decay: 40                       # dynamic only: solve count at which value hits points_min

max_attempts: null              # optional; null/omitted = unlimited
visible: true                   # seed as visible

# --- container kind only ---
kind: container                 # standard | container (default standard)
image: osctf/example-cookie-monster:0.1
internal_port: 5000
mem_limit_mb: 128               # optional (16–8192, default 256)
cpu_millis: 500                 # optional (100–8000, default 500; 1000 = one core)
connection_template: "http://{host}:{port}"   # optional; default "nc {host} {port}"
inject_flag: true               # optional, default true
container_env:                  # optional extra env for the image
  DIFFICULTY: normal

# --- attachments (both kinds) ---
files:                          # optional; paths relative to this dir
  - files/capture.pcap
```

Validation mirrors the database constraints: `standard` challenges must not set
`image`/`internal_port`; `container` challenges require both; dynamic scoring
requires `points_min` and `decay`.

## Standard vs container challenges

- **standard** — description, a static flag, and optional attachments. No
  container. Use for crypto, forensics, rev, and any web/pwn task that ships a
  file rather than a live service.
- **container** — also runs one Docker container per challenge, exposed on a
  host port. Build layout:

  ```
  examples/challenges/<slug>/
  ├── challenge.yaml
  ├── files/          # attachments handed to players (source, pcaps, binaries)
  └── src/            # image build context (Dockerfile + app)
  ```

  `make examples` runs `docker build -t <image> src/` for every container
  challenge; the tag must match `image:` in the yaml. Images are built locally
  and **not** pushed in v0.1 — deploy them from the admin instance panel.

## Flag injection — never bake the flag into an image

The platform injects the flag as the `FLAG` environment variable at deploy time,
merged over `container_env`. Your image reads `os.Getenv("FLAG")` (or the
equivalent). This lets organizers rotate a flag without rebuilding, and keeps the
flag out of the image layers.

`env-hunter` is the canonical example: `docker history` / `strings` on its image
show **no** flag; the flag only exists in the container's environment at runtime.

## Shared-instance caveats (important)

v0.1 runs **one shared container per challenge** — every team hits the same
instance. Design accordingly:

- **No per-team secrets or per-team state.** A flag is the same for everyone.
- **No destructive shared writes.** If a player can corrupt shared state, they
  ruin the challenge for everyone. `cookie-monster` and `robots-rule` are
  stateless by design — copy that pattern. If you need persistence, have the
  image self-reset periodically.
- Per-team isolated instances (with unique per-team flags) arrive in **v0.2**.

## Isolation limits (read before writing pwn)

Challenge containers run with `no-new-privileges`, all Linux capabilities
dropped, memory/CPU/pid limits, and the Docker default seccomp/AppArmor
profiles — but they are **still plain containers sharing the host kernel**.

- **Kernel-level or destructive pwn is out of scope** for v0.1's shared Docker
  isolation. A kernel-exploit challenge can escape the container. Keep pwn gentle
  (`overflow-lite` is a deliberately simple userspace stack overflow).
- Container egress is **enabled** in v0.1 (many web challenges fetch things).
  Do not rely on network isolation between challenge containers.
- Stronger isolation (gVisor/Firecracker, `--internal` networks) is a v0.2
  hardening item.

## Testing locally

```bash
make examples                                    # build container images
docker compose up -d --build                     # seeds your new challenge
# In the admin panel: open the challenge, Deploy the instance, and solve it
# exactly as a player would. An example that isn't solvable is a release blocker.
```
