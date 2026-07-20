# 07 — Scoring & Scoreboard

## Engines

Two `ScoringEngine` implementations, selected per challenge by its `scoring` column. Both are **pure functions** in `api/internal/scoring` — no I/O, no clock, exhaustively unit-tested.

### Static

```
Value(params, solves) = params.Initial
```

### Dynamic (solve-count decay, CTFd-compatible shape)

```
Value(params, solves) = max(Min, round( Initial - (Initial - Min) * solves² / Decay² ))
```

- `solves` = number of **valid solves** (defined below) of the challenge, *including* the solver being displayed — everyone gets the same current value; earlier solvers are re-valued downward as more teams solve (standard CTF behavior).
- `Decay` = the solve count at which the value reaches `Min`.
- `round` = round-half-up to the nearest integer (`math.Round`).
- Guard: `solves < 0` is treated as 0; the `max` clamp handles `solves > Decay`.

Worked examples (`Initial=500, Min=100, Decay=50`):

| solves | value |
|---|---|
| 0 | 500 |
| 1 | 500 − 400·(1/2500) = **500** (rounds from 499.84) |
| 5 | 500 − 400·(25/2500) = **496** |
| 10 | 500 − 400·(100/2500) = **484** |
| 25 | 500 − 400·(625/2500) = **400** |
| 50 | **100** (reaches Min) |
| 80 | **100** (clamped) |

These exact numbers are test vectors — encode them in `scoring/dynamic_test.go`.

Defaults for new dynamic challenges (admin UI prefills, overridable): `Initial=500, Min=100, Decay=25`.

## Valid solves

A solve (a `submissions` row with `correct=true`) counts toward solve counts **and** standings iff, at computation time:

- the team is not `hidden` and not `banned`, and
- the team has at least one member who is not `hidden` (excludes admin test teams), and
- the challenge is currently `visible`.

Note the *at computation time*: hiding a team or challenge retroactively re-values dynamic challenges for everyone. This is intended (it's the anti-cheat lever) and is why standings are always recomputed from the ground truth rather than incrementally adjusted.

## Standings computation (the one true algorithm)

Implemented in `scoreboard.Compute(ctx)`; called on: correct submission, admin changes to challenges/teams/users/event, cache miss, and freeze snapshot.

1. One SQL query: all valid solves as `(team_id, challenge_id, solved_at)` joined with team name/banned and challenge scoring params, excluding hidden/banned/invisible per the rules above (banned teams: excluded from *solve counts*, but see step 4).
2. In Go: group by challenge → `solveCount[challenge]`; compute `value[challenge] = engine.Value(params, solveCount)`.
3. Per team: `points = Σ value[challenge]` over its solves; `last_scoring_solve_at = max(solved_at)`; `solves = count`.
4. Banned teams are *displayed* (struck-through, per [`04-database.md`](04-database.md)) with their points computed against the same values but **their solves did not contribute to solve counts in step 2**. They still occupy a rank row; frontend renders them last within equal points? No — simpler and final: banned teams are listed **after** all non-banned teams regardless of points, with `banned: true`, unranked (`rank: null`).
5. Sort non-banned: `points DESC, last_scoring_solve_at ASC, team name ASC` (name only as a deterministic final tiebreak for zero-score teams). Assign `rank` 1..N — **standard competition ranking is not used**; ties are fully broken by time, so ranks are unique.
6. Teams with zero solves appear with 0 points (every non-hidden team is on the board from creation).

Serialize to the scoreboard JSON payload ([`05-api.md`](05-api.md)) with `generated_at` (from the injected clock) and write `scoreboard:current` in Redis. Computation is O(solves) and runs in single-digit milliseconds at MVP scale — recompute-from-scratch beats incremental cleverness; do not optimize past this in v0.1.

Concurrency: serialize recomputes with a per-process mutex + a "dirty" flag (a recompute requested while one runs schedules exactly one follow-up). Correctness does not depend on this — it's only to avoid wasted work.

## Freeze

- `events.freeze_at` (nullable). At `now >= freeze_at`, the freeze/phase ticker (15 s) writes `scoreboard:frozen` **once** (copy of the then-current snapshot) if absent.
- After freeze: non-admin REST reads and all WS broadcasts serve the frozen snapshot; live recomputes continue internally (admins read live via REST; `GET /scoreboard` for an admin session returns live data with `frozen: false`... **no** — returns live data with `frozen: true` so admin UI can still show the banner. The `frozen` field describes the event state, not the payload source. Admin payloads are live; the field says the *public* board is frozen).
- Clearing `freeze_at` (admin PATCH) deletes `scoreboard:frozen` — the board unfreezes and jumps to live.
- Submissions after freeze count normally — freeze affects display only.

## Points shown on challenges

`GET /challenges` shows each challenge's *current* value (step 2's `value[challenge]`) and total solve count. During freeze these also freeze for non-admins? **No** — challenge list stays live (only the *standings* freeze; solve counts moving is normal at CTFs). Keep the frozen snapshot strictly about standings.

## What changes a score (recompute triggers, exhaustive)

| Trigger | Where |
|---|---|
| Correct flag submission | submissions service, post-commit |
| Challenge created/edited (scoring fields, visibility)/deleted | challenges service |
| Team banned/hidden toggled | admin teams service |
| User hidden toggled (affects all-hidden-members rule) | admin users service |
| Event freeze_at changed | events service (cache/frozen bookkeeping only) |

Each trigger: recompute → write cache → WS broadcast (throttled ≥ 1 s, latest-wins).

## Manual point adjustments

Out of scope for v0.1 (no adjustments table). The workaround for organizers is documented in the event-running guide: create a hidden `misc` challenge worth N points and mark it solved by the team via a flag you give them. Revisit in v0.2.
