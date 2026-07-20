# 09 — Frontend (Dashboard)

Vite + React 19 + TS strict, stack details in [`03-tech-stack.md`](03-tech-stack.md). Served in production as static files embedded in the Go binary (SPA fallback to `index.html`); in dev via `npm run dev` on :5173 with a Vite proxy for `/api` and `/api/v0/ws` → :8080.

## Source layout

```
dashboard/src/
├── main.tsx  App.tsx  router.tsx
├── api/
│   ├── schema.d.ts        # generated (openapi-typescript) — committed
│   ├── client.ts          # openapi-fetch instance + queryKeys factory + ApiError normalizer
│   └── hooks/             # useMe, useChallenges, useSubmitFlag, useScoreboard, useAdmin*
├── ws/scoreboard-socket.ts# reconnecting WS client (below)
├── components/
│   ├── ui/                # vendored shadcn-style primitives (button, input, dialog, table, badge, tabs, toast)
│   └── …                  # app components (below)
├── pages/                 # one file per route
├── lib/                   # time formatting (relative + UTC tooltips), countdown hook, markdown renderer
└── styles/                # tailwind entry, tokens
```

## Route table

| Path | Page | Guard | Contents |
|---|---|---|---|
| `/` | Landing | — | Event name/description (markdown), phase-aware countdown (to start, to end, or "ended"), CTA buttons (register/login or challenges) |
| `/login`, `/register` | Auth forms | redirect away if authed | Forms with field-level errors from problem+json |
| `/challenges` | Challenge board | user + event started | Cards grouped by category; card: title, points (live), solve count, difficulty badge, solved-✓ state. Click → detail dialog |
| `/challenges/:slug` | Challenge detail (routed dialog over the board) | user | Markdown description, attachments (download links), connection info (copy button) when instance running, flag input + submit, attempts counter when max_attempts, per-field feedback: correct → confetti-free ✓ + points, wrong → shake, 429 → cooldown message with countdown |
| `/scoreboard` | Scoreboard | — | Live table: rank, team (link), points, solves, last solve (relative). Frozen banner when frozen. Banned teams struck-through at bottom |
| `/team` | My team | user | No team: create + join (invite code) forms. With team: members, invite code (captain: copy + regenerate), rename (captain), leave (confirm dialog), team solves |
| `/teams/:id` | Public team | — | Name, members, rank/points, solve list with times |
| `/users/:id` | Public profile | — | Username, team link, solve list |
| `/profile` | Settings | user | Change password form |
| `/admin` | Admin dashboard | admin | Stat tiles from `/admin/stats`, quick links |
| `/admin/event` | Event settings | admin | Name, description, start/end/freeze datetime pickers (UTC-explicit), validation errors |
| `/admin/challenges` | Challenge list | admin | Table: title, category, kind, points, solves, visible toggle (inline PATCH), instance state badge; filters; "New challenge" |
| `/admin/challenges/new`, `/admin/challenges/:id` | Challenge editor | admin | Full form incl. kind switch (container fields appear), flag field (password-style with reveal), markdown preview tab, attachments panel (upload/delete), **instance panel** (deploy/restart/stop/destroy buttons, state, port, connection info, logs viewer with refresh) |
| `/admin/users` | Users | admin | Paginated table, search; ban/hide toggles, role select, reset-password dialog |
| `/admin/teams` | Teams | admin | Paginated table, search; ban/hide toggles |
| `/admin/submissions` | Submission log | admin | Paginated table with filters (challenge, team, correct, time range); shows provided flag + IP; auto-refresh toggle (10 s) |
| `*` | 404 | — | Link home |

Guards: a `RequireAuth` / `RequireAdmin` layout route reads `useMe()`; unauthenticated → redirect `/login?next=<path>`; non-admin on admin routes → 404-style page (don't confirm admin routes exist). `useMe` (GET `/auth/me`) is the session source of truth — no client-side token storage of any kind.

## Data & errors

- All reads via TanStack Query with the queryKeys factory; all writes via mutations that invalidate affected keys (`submitFlag` invalidates `challenges`, `scoreboard`, `me/team` keys).
- 401 from any query → global handler clears cache and redirects to login (except on public pages).
- problem+json → `ApiError { title, detail, fields }`; forms map `fields`; non-form errors render a toast. Never show a raw 500 body; show "Something broke — request id `<X-Request-Id>`".

## Scoreboard live updates

`ws/scoreboard-socket.ts`:

- Connect to `ws(s)://<host>/api/v0/ws` on scoreboard/challenge pages (single shared connection, ref-counted).
- On `scoreboard` message → write payload straight into the TanStack Query cache for the scoreboard key (`setQueryData`) and invalidate `challenges` (points/solve counts move).
- Reconnect: exponential backoff 1 s → 30 s with jitter; while disconnected, fall back to polling `GET /scoreboard` every 30 s; on reconnect, stop polling.
- `event.phase` message → invalidate `event` + `challenges` queries (board unlocks at start without a manual refresh).

## Design system

- **Dark theme default** (CTF audience), light theme via `prefers-color-scheme` + manual toggle persisted in `localStorage`. Both themes required from day one — build with CSS variables, not `dark:` sprinkled ad hoc.
- Tailwind v4 tokens (CSS variables): `--background --surface --surface-2 --border --text --text-muted --primary (indigo-500 family) --success --danger --warning`, category accent colors: web=sky, pwn=red, crypto=violet, rev=amber, forensics=emerald, misc=slate.
- Typography: system font stack (`ui-sans-serif`); monospace (`ui-monospace`) for flags, connection strings, code, logs.
- Density: comfortable tables, `max-w-6xl` centered layout, sticky top nav (logo/event name · Challenges · Scoreboard · Team · avatar menu; admin link when admin).
- States: every async view has explicit loading (skeletons), empty ("No challenges yet — check back at start time" etc.), and error states. No spinner-only pages.
- Accessibility: keyboard-navigable dialogs (Radix), visible focus rings, WCAG AA contrast in both themes, `aria-live="polite"` on submission feedback and scoreboard updates.

## Timestamps

All times render in the **viewer's local timezone** with a UTC absolute on hover (tooltip); countdowns tick client-side against server timestamps (never trust client clock for gating — the server enforces; the UI only displays).

## Testing hooks

Add `data-testid` on: flag input, submit button, scoreboard table, team invite code, admin deploy button — the Playwright specs in [`11-testing-ci.md`](11-testing-ci.md) rely on these exact ids: `flag-input`, `flag-submit`, `scoreboard-table`, `invite-code`, `instance-deploy`.
