# 07 — Frontend

Same stack (React 19 + TS strict + Vite + Tailwind v4 + TanStack Query + openapi-fetch,
embedded in the binary via `embed_spa`). v0.2 adds a **participant instance panel** to the
challenge dialog and an **admin Instances page**. The regenerated `openapi-typescript`
client gives the new types for free; the drift gate keeps it honest. Baseline:
[`../v0.1/09-frontend.md`](../v0.1/09-frontend.md).

## Participant: instance controls in the challenge dialog

Extend [`ChallengeDialog.tsx`](../../dashboard/src/components/ChallengeDialog.tsx). Today it
shows `connection_info` (for shared instances) + the flag form. v0.2 adds a
**`<InstancePanel>`** (participant variant, distinct from the admin one) rendered when
`chal.instancing === 'per_team'`, driven by `chal.instance` (`TeamInstance | null`) from
`getChallenge`.

States and controls:

| `chal.instance` | Panel shows |
|---|---|
| `null` | **Start** button (`instance-start`). Copy: "Start your instance". |
| `state: pending`/`starting` | Spinner + "Starting…", poll `getChallenge` every ~2 s until settled. |
| `state: running` | `connection_info` (existing copy-button UI), a **countdown** to `expires_at`, **Extend** (`instance-extend`) and **Stop** (`instance-stop`). |
| `state: error` | Error text + **Retry** (re-Start). |
| `state: stopped`/`lost` | Treated as gone → show **Start** again. |

New hooks in `dashboard/src/api/hooks.ts` (mirror the admin instance hooks): `useStartInstance(slug)`,
`useStopInstance(slug)`, `useExtendInstance(slug)`. On success each invalidates the
`getChallenge` query (and the challenge list badge). While an instance is `pending` or has a
live countdown, keep a light `refetchInterval` on `getChallenge` (or react to the WS
`instance` nudge from [`06-api.md`](06-api.md)); drop it once `running` and past settle.

Countdown: a small `useCountdown(expires_at)` returning `mm:ss`; at ≤ 5 min, style it
`text-warning`; at `0`, show "expired — start again" and refetch. `expires_at: null`
(challenge with `instance_ttl_seconds=0`) → hide the countdown and Extend entirely.

Quota / event errors surface inline (not a toast that vanishes): a `409 quota-exceeded`
renders "You have N/${limit} instances running — stop one to start another." A `409
event-not-running` renders "Instances are available while the event is running."

## Participant: per-instance flag challenges

No special UI beyond the panel — the flag form is unchanged. The only new behaviour is the
server's `403 no-instance` on submit for a `per_instance` challenge with no running
instance; the existing `FeedbackLine` renders its problem detail ("Start the challenge
first"). Because Start is right there in the same dialog, the user recovers in one click.

## Admin: Instances page (fleet observability)

New route `/admin/instances` + an **Instances** entry in
[`AdminNav.tsx`](../../dashboard/src/pages/admin/AdminNav.tsx). New page
`dashboard/src/pages/admin/AdminInstancesPage.tsx` backed by `adminListInstances`
(`useAdminInstances()` hook, `refetchInterval` ~5 s for a live view).

A table (`data-testid="admin-instances-table"`), one row per instance (shared + per-team):

| Column | Source |
|---|---|
| Challenge | `challenge_slug` (link to the admin editor) |
| Owner | `team_name` or **shared** badge when `team_id == null` |
| State | colored `state` (reuse `stateColor` from the admin `InstancePanel`) |
| Port | `host_port` |
| Network | `network` (or "—" for shared) |
| Age | from `started_at` (reuse `formatRelative`) |
| Expiry | countdown from `expires_at`, or "—" |
| Health | `last_health_at` relative |
| Actions | **Destroy** (`admin-instance-destroy`) → `adminDestroyInstanceById` |

Filters (client-side, cheap): by owner kind (all / shared / per-team) and by state. The page
**never** shows a flag (there is no flag field in `AdminInstance`).

The existing per-challenge admin `InstancePanel`
([`InstancePanel.tsx`](../../dashboard/src/pages/admin/InstancePanel.tsx)) stays as-is for
**shared** challenges in the challenge editor. For a `per_team` challenge the editor hides
Deploy/Restart (those are participant/scheduler-driven) and instead links to
`/admin/instances?challenge=<slug>`.

## Admin: challenge editor authoring fields

Extend [`AdminChallengeEditor.tsx`](../../dashboard/src/pages/admin/AdminChallengeEditor.tsx)
with the new fields, shown **only when `kind === 'container'`** (mirrors the DB/API
constraint so the form can't submit an invalid combo):

- `instancing` select — Shared | Per-team (`ch-instancing`).
- `flag_mode` select — Static | Per-instance (`ch-flag-mode`), enabled only for container.
- `instance_ttl_seconds` number (`ch-instance-ttl`), placeholder "default", `0` = no TTL.
- `egress` checkbox (`ch-egress`), default on.
- `writable_paths` — a simple comma/newline list (`ch-writable-paths`).

Keep the existing `htmlFor`/`id` label-association pattern (the v0.1 e2e fix) for every new
control so `getByLabel` works in tests.

## Testids (for e2e — [`09-testing-ci.md`](09-testing-ci.md))

| testid | Element |
|---|---|
| `instance-start` | Participant Start button |
| `instance-stop` | Participant Stop button |
| `instance-extend` | Participant Extend button |
| `instance-state` | Participant instance state text |
| `instance-countdown` | Participant TTL countdown |
| `admin-instances-table` | Admin fleet table |
| `admin-instance-destroy` | Admin destroy action |
| `ch-instancing`, `ch-flag-mode`, `ch-instance-ttl`, `ch-egress`, `ch-writable-paths` | Editor fields |

## State handling notes

- Never trust a stale instance object across the countdown reaching 0 — always refetch on
  expiry rather than assume.
- Optimistic UI is unnecessary here; Start/Stop are quick server round-trips and the
  panel's states are explicit. Show pending/disabled during the mutation, invalidate on
  settle.
- The panel is a small addition to an existing dialog — do not build a separate route.

## Decision log

- **Instance panel lives inside the existing challenge dialog.** One place for connection
  info, countdown, controls, and the flag form; Start-to-solve is a single view.
- **Poll `getChallenge` while pending/counting; optional WS nudge.** Reuses the v0.1
  data-fetching model; secrets never ride the socket.
- **Authoring fields gated on `kind==='container'` in the form.** The UI can't produce an
  invalid instancing/flag-mode combo, matching the server's `422`.
- **Admin fleet page is read-plus-destroy only.** Per-team spawn is participant-driven; the
  admin's job is observe + intervene, not micromanage every team's Start.
