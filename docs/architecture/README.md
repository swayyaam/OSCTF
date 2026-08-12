# Architecture diagrams

Five Excalidraw canvases describing OSCTF **as built**, verified against commit `6c3e2f0`
(`main`). Open them at [excalidraw.com](https://excalidraw.com) (File → Open) or with the
Excalidraw VS Code extension.

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
| [`01-flow-submission`](01-flow-submission.excalidraw) | `submissions.Submit` | Why the check order inside the transaction is load-bearing, and where a plugin is allowed to be called |
| [`02-flow-scoreboard`](02-flow-scoreboard.excalidraw) | `scoreboard` + repair worker | How the served board is kept equal to the solve log by construction |
| [`03-flow-instance`](03-flow-instance.excalidraw) | `scheduler` + `runtime` | How a per-team container is born, extended, expired and reaped |
| [`04-flow-plugin`](04-flow-plugin.excalidraw) | `internal/plugin` | The full plugin state machine, call path, and shutdown ordering |

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
