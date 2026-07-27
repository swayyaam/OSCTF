# Running an Event

A start-to-finish timeline for hosting a CTF on OSCTF. Assumes you have completed
[`install.md`](install.md) and can log in as the admin.

## 1. Configure the event (days before)

**Admin → Event.** Set the name, a markdown description (shown on the landing
page), and the start/end times. **All times are UTC** — the pickers are
UTC-explicit and the countdown on the landing page ticks against them.

Leave **Freeze** empty for now (see step 6).

While the event is in the `pre` phase, the challenge board is closed to players
(they see a countdown); admins can preview it.

## 2. Load challenges (days before)

Two ways:

- **Seeded** — drop challenge directories in `examples/challenges/` and
  (re)launch on a clean database; the first-boot seeder loads them. See
  [`authoring.md`](authoring.md).
- **Admin panel** — **Admin → Challenges → New challenge.** Fill the form; for
  container challenges add the image + internal port, then use the instance panel
  to Deploy.

Keep new challenges `visible: false` until you have tested them.

## 3. Test everything (the day before)

For **every** challenge, solve it yourself exactly as a player would:

- standard: download attachments, solve, submit the flag → correct.
- container: Deploy the instance, connect via the shown connection string, solve,
  submit. Watch the instance panel — state should be `running`; use the logs
  viewer if it isn't.

An unsolvable challenge is worse than no challenge. Do not skip this.

Also do a full participant dry-run in a second browser: register → create a team
→ open the board → submit a wrong then a right flag → confirm the scoreboard
moves.

## 4. Open registration / go live

Registration is open by default (`OSCTF_REGISTRATION_OPEN=true`). To gate it, set
that to `false` and flip it on when you're ready.

At the start time the board unlocks automatically — no manual step. The event
phase transition is pushed to connected clients (the board unlocks without a
refresh).

Make every challenge you intend to run `visible`. For container challenges,
Deploy their instances shortly before start (or leave them up from testing).

## 5. During the event

- **Monitor** — Admin dashboard tiles (users, teams, submissions, solves,
  running instances, live connections). If you enabled the observability profile,
  Grafana shows request latency, submission rate, and instance states.
- **Submissions log** — Admin → Submissions. Every attempt is logged with the
  team, user, provided flag, and source IP. Turn on auto-refresh to watch live.
  This is your anti-cheat lever alongside the built-in rate limits (10/min per
  team+challenge, 30/min per user).
- **Anti-cheat** — to neutralize a cheating team, **hide** or **ban** it (Admin →
  Teams). Hidden/banned teams are removed from solve counts, which *re-values
  every dynamic challenge for everyone* — this is intentional. Banned teams stay
  on the board struck-through and unranked.
- **Manual point adjustments** are not built in v0.1. The workaround: create a
  hidden `misc` challenge worth N points and give the target team a flag for it.

## 6. Freeze the scoreboard (near the end)

To build suspense, set **Freeze** (Admin → Event) to a time near the end (e.g. 1
hour before close). After the freeze point:

- Players see the **frozen** snapshot with a banner; it stops moving even as new
  solves land.
- Admins still see live standings (with the frozen banner, so you know the public
  view is frozen).
- Challenge point values and solve counts keep moving — only the *standings*
  freeze.

Clearing Freeze unfreezes the board and jumps it to live.

## 7. Close and wrap up

At the end time the board moves to the `ended` phase. Submissions stop counting
(the event window is enforced server-side).

Challenge containers are **not** stopped automatically — organizers often keep
them up for practice. To tear them down:

```bash
docker ps --filter label=osctf.managed=true         # see what's running
# then use the admin instance panel's Destroy button per challenge
```

Take a final backup (see [`install.md`](install.md#backups)), export the
submissions log if you need it, and collect feedback for the next event.
