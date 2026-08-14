# Architecture diagrams

Five Excalidraw canvases describing OSCTF **as built**, verified against commit `92c5755`
(`main`). Open them at [excalidraw.com](https://excalidraw.com) (File → Open) or with the
Excalidraw VS Code extension.

**These `.excalidraw` files are the source of truth.** The PNGs embedded in the root
[`README.md`](../../README.md) are periodic exports of these canvases; a render always lags its
source between exports, so if a PNG and a canvas disagree, the canvas wins. Each canvas stamps
the commit it was verified at in its subtitle (top-left).

Anything specced but **not** built at that commit is drawn with a dashed grey outline and
labelled — the diagrams are a picture of the code, not of the roadmap.

## How the files relate

[`00-overview.excalidraw`](00-overview.excalidraw) is the entry point and stands alone. It has
three bands, read top to bottom; **stop after any band and you still have a correct mental
model.**

| Band | Answers |
|---|---|
| 1 · The 10-second view | Who talks to OSCTF, and what OSCTF talks to. Readable without knowing Go. |
| 2 · Inside the binary | The real internal packages and how a request flows through them. |
| 3 · Where state lives | What survives a restart, and what deliberately does not. |

The four flow files each zoom into one path that band 2 only summarises. They are siblings —
read in any order, none depends on another — and each repeats its entry box from the overview
so it makes sense on its own.

| File | Zooms into | Read it when you want to know |
|---|---|---|
| [`01-flow-submission`](01-flow-submission.excalidraw) | `submissions.Submit` | Why the check order inside the transaction is load-bearing, where a plugin is allowed to be called, and how the rate limits fail closed when Redis is down |
| [`02-flow-scoreboard`](02-flow-scoreboard.excalidraw) | `scoreboard` + repair worker | How the served board is kept equal to the solve log by construction, and how a read degrades to Postgres (while a freeze fails closed) during a Redis outage |
| [`03-flow-instance`](03-flow-instance.excalidraw) | `scheduler` + `runtime` | How a per-team container is born, extended, expired and reaped — and how the isolation gate refuses a deploy when per-team isolation can't be verified |
| [`04-flow-plugin`](04-flow-plugin.excalidraw) | `internal/plugin` | The full plugin state machine, call path, shutdown ordering, and the boot-time writable-plugins-dir check |

## Conventions

- **Colour** — blue: external client · grey: in-process · green: datastore · orange:
  out-of-process plugin · purple: background worker · dashed grey outline: specced, not built.
- **Lines** — **solid** is a synchronous request path; **dashed** is asynchronous,
  best-effort, or running on a detached context. That distinction is load-bearing: almost
  every dashed edge is something that can be dropped or retried without failing a request.
- Every arrow is labelled with what flows over it (HTTP, gRPC, SQL, Redis protocol, Docker
  API, WebSocket, Go call).

## Keeping them honest

These were generated once and are now **hand-edited source** — there is no generator to
re-run, so edit the `.excalidraw` files directly.

Each canvas carries the commit it was verified at in its subtitle. If you change something
these describe, update the drawing and bump that stamp. A diagram that silently drifts from
the code is worse than no diagram: it is confidently wrong, and it is the first thing a new
reader trusts.

When you edit a canvas, also **re-export its PNG** to [`docs/public/`](../public/) (Excalidraw:
File → Export image → PNG, 2×, light background) and bump the export-commit stamp in the root
[`README.md`](../../README.md) caption — otherwise the picture a reader sees lags the source.

`make diagram-staleness` compares each canvas's stamped commit against `HEAD` and flags when
non-test code under `api/` has changed since — a nudge to re-verify, not a gate (it always exits
0; it runs at the end of `make ci-local` as an advisory line). It deliberately does **not** map
diagrams to packages (such a map would itself drift); it treats the whole `api/` tree as "code
the diagrams describe," so it errs toward warning. It is not noise: pointed at a canvas still
stamped `6c3e2f0`, it flags exactly the 13 files the v0.3 security pass changed unremarked — the
drift a manual audit caught 14 commits late (negative control recorded in the script header).
