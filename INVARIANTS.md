# OSCTF Invariants

A readable statement of the guarantees the platform holds — the things that are true no matter what,
that a new contributor should read **before touching anything**.

**This file is a summary, not the source of truth.** Each guarantee below is *enforced* by a named
test, and those tests are authoritative: the [testing contract in `AGENTS.md`](AGENTS.md) declares
them "never weakened, skipped, build-tagged off, or deleted to make a build go green." This file only
*describes* them in prose. If this file and a test disagree, **the test is right and this file is
wrong** — fix the file. Do not add a guarantee here that no test enforces; write the test first, or
it is aspiration, not an invariant. Keeping the enforcement in the tests (and this description
separate) is deliberate: a second place where invariants "live" drifts from the code within a
release.

The security-specific view — adversaries, what each can reach, what is accepted — is
[`THREAT_MODEL.md`](THREAT_MODEL.md).

---

## Postgres is the authority; everything else is derived

Postgres holds the truth: users, teams, the append-only submission log (including each plugin-scored
solve's `scored_value`/`scored_by`), instances, and tokens. Everything else is reconstructible from
it. Redis (sessions, rate-limit buckets, the scoreboard cache) can be lost and rebuilt — the served
scoreboard is always a from-scratch recompute over the Postgres log. Docker state is reconciled
*against* Postgres rows, never treated as authority. The **one** piece of derived state whose loss is
not silently recoverable is the **freeze snapshot**, and it fails closed rather than serving a live
board. *Pinned by:* `scoreboard.TestCurrentReadRepairServesFreshWhenCacheBehind` +
the soak's served-equals-log invariant; `handlers.TestFreezeFailsClosedWithoutEvents`;
`runtime.TestReconcileDecisions`.

## Plugins are isolated processes, and a plugin's failure is not the platform's

Every plugin runs as a separate OS process over gRPC. Boot never gates HTTP serving; one slow,
hung, or crashing plugin cannot stall another plugin or the host. And each plugin type fails in its
*safe* direction: a challenge-type outage rejects-and-retries with **no attempt consumed**, scoring
is off the read path (a fallback/pending value is recorded, the board stays recomputable), and a
notification drop is counted while the action still commits. *Pinned by:*
`cmd/platform.TestBootDoesNotGateServing`, `plugin.TestPerPluginCapDoesNotDelayOtherPlugins`,
`plugin.TestCrashOnLaunchQuarantinesAtCap`; `submissions.TestPluginRejectRetryConsumesNoAttempt`,
`scoreboard.TestComputeReadsPluginRecordNotFormula`, `events.TestBackpressureDropsCounted`.

## Plugins never receive secrets

A challenge-type plugin's `CheckFlag` receives the participant's **guess, never the flag**; scoring
receives curve parameters `(initial, min, decay, solves)`; notifications receive explicitly
non-secret, non-PII event data. No flag, per-instance secret, or token reaches any plugin (or any
other participant-facing surface). *Pinned by:* `handlers.TestFlagContainmentIntegration`.

## Per-team lifecycle is serialized

A per-team lock serializes a team's instance operations, and the submission path cannot double-count
a concurrent double-solve — one solve, one row, regardless of interleaving. *Pinned by:*
`scheduler.TestTeamLockMutualExclusionUnderChurn`,
`scheduler.TestSchedulerStartIdempotentAndQuotaIntegration`,
`handlers.TestConcurrentDoubleSolveRace`,
`submissions.TestSubmissionsConcurrentSolveNoDoubleCountProperty`.

## Docker state is reconciled, never trusted

The daemon is ground truth to *align with the DB*, not an authority: orphan containers (no row) are
removed, containers whose `osctf.instance_id` label is missing are flagged but never removed (they
cannot be safely identified), and empty per-team bridges are GC'd only when the team has no fresh
row. Adoption keys purely on the label, so it survives restarts and upgrades. *Pinned by:*
`runtime.TestReconcileDecisions`, `runtime.TestLabelContract`, and the `scheduler` reconcile
integration suite.

## Scoreboard reads are deterministic

The served board equals a from-scratch recompute over `(the Postgres solve log + the per-solve
scoring records)` — at every read, regardless of plugin state. Built-in `static`/`dynamic` recompute
from the formula; plugin modes are **locked at solve** and read from the record, so the read path
never calls a plugin and the board is correct even with every plugin down. *Pinned by:*
`scoreboard.TestComputeReadsPluginRecordNotFormula`,
`scoreboard.TestScoringRegistryStandingsEquivalence`, the soak's served-equals-recompute and
record-within-latency-bound invariants (with the `-recompute-via-plugin` and `-break-score-record`
negative controls).

## Authorization fails closed

Every route has an authorization-policy entry or CI fails; `requireAdmin` re-reads the user row on
every call (so a ban/demotion takes effect on the next request) and refuses anything that is not an
existing, non-banned admin; a missing dependency **hides** rather than leaks; a hidden or unreleased
resource is indistinguishable from a nonexistent one (status, body, ~timing). *Pinned by:*
`handlers.TestPolicyTableCoversEveryRoute`, `handlers.TestPolicyMatrixIntegration`,
`handlers.TestFreezeFailsClosedWithoutEvents`, `handlers.TestEnumerationHiddenChallengeIndistinguishable`.

## Resource budgets are shared and bounded

One file-descriptor accountant splits the process budget across WebSockets and plugins, with the
DB/Docker/Redis reserve counted exactly once; argon2id hashing is concurrency-bounded and sheds with
`503` rather than OOMing; the plugin in-flight budget is two-level (per-plugin then global).
*Pinned by:* `cmd/platform.TestDeriveResourceBudgetReservesExactlyOnce`,
`auth.TestHashGateBoundsConcurrency` / `auth.TestHashGateShedsWhenFull`, the `plugin` in-flight tests.

## Container challenges are refused when per-team isolation is unenforced

A container instance does not start on a Docker daemon that cannot isolate it from other teams
(Docker Desktop, or an unverified daemon) — the deploy fails closed. The only escape is an explicit,
loudly-logged `OSCTF_ALLOW_UNISOLATED_INSTANCES=true`, for a local trial. *Pinned by:*
`runtime.TestIsolationGate`. (Detail: [`THREAT_MODEL.md`](THREAT_MODEL.md) §2.)

---

## The one that is a tradeoff, not a virtue: async work happens after commit

Three effects run **after** the submission transaction commits, on best-effort paths a crash can
interrupt, a slow consumer can delay, and a full queue can drop:

1. the **scoreboard recompute**,
2. the per-solve **scoring-record write**,
3. the domain-**event publish** (notifications).

This is **not a virtue** — do not describe it as one. It is the tradeoff that produced the
scoreboard-versus-log flake ([#6](https://github.com/swayam-mishra/OSCTF/issues/6)), the missing
scoring-record case, and notification drops. Because a post-commit effect is best-effort, **every one
of them must be independently recoverable**, and each has a *named* repair mechanism:

| Post-commit effect | Why it can be lost | Repair mechanism (named) | Pinned by |
|---|---|---|---|
| Scoreboard recompute | a missed/slow/preempted recompute | **read-repair** — a served snapshot records the solve count it was built from; a read that finds the log has moved past it recomputes before serving | `scoreboard.TestCurrentReadRepairServesFreshWhenCacheBehind` + the soak `-break-readrepair` control |
| Scoring-record write | crash between commit and the record write | **the off-read-path repair worker** — backfills missing/pending records on a bounded tick | `submissions.TestScoreRepairBackfillsMissingRecord` + the soak record-within-bound invariant + `-break-score-record` |
| Notification publish | a full or dead subscriber | **counted drops** — best-effort by contract; a drop never gates the solve and is always counted, by reason | `events.TestBackpressureDropsCounted` |

**The rule this section exists to enforce: a new post-commit path ships with its repair mechanism, or
it does not ship.** Someone will add a fourth — a webhook, an outbox, a cache invalidation. This is
the sentence that stops it shipping best-effort with no way to recover what it drops.

---

## Keeping this honest

The tests are authoritative; this file is their readable index. When you change a guarantee, update
its test and the line here in the same change. When you add a guarantee here, add the test that pins
it first — a claim with no test is not an invariant, and this file is worthless the moment it says
something the code does not do. `AGENTS.md`'s testing contract is where these tests are declared
un-weakenable; start there if you are about to touch one.
