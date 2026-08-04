//go:build integration

package scheduler_test

// Phase 5 property tests: randomized, seeded operation sequences that assert the
// scheduler's global invariants after EVERY step (not just at the end), targeting
// the state space the soak does not reach — adversarial orderings and rapid
// start/stop/extend interleavings against a single instance. On a violation the
// test stops and prints the seed + the full op log so the sequence replays and can
// be minimized by hand. These drive the scheduler SERVICE directly; see the report
// note on which findings would also be reachable through the HTTP handlers.

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/scheduler"
)

// propPortLo/propPortHi mirror newHarness's port window (30000..30999).
const (
	propPortLo = 30000
	propPortHi = 30999
)

// propInvariants reads committed DB + fake-runtime state and returns every violated
// global invariant (empty = all hold). Call it only at a quiescent point: after an
// op returns in a single-goroutine sequence, or after joining goroutines. The
// invariants:
//   - state legality: every row is in a known lifecycle state.
//   - port uniqueness + range: no host_port held by two rows; all within the window.
//   - single instance: at most one row per (team, challenge) — the per-team unique index.
//   - quota: no team exceeds its RUNNING-instance cap (CountTeamRunningInstances counts
//     state='running', so the invariant counts running rows only).
//   - max lifetime: a running instance's expires_at never exceeds StartedAt + MaxTTL,
//     the bound Extend caps against.
//   - no orphan container: every fake container maps to an existing instance row.
func propInvariants(ctx context.Context, h *harness, quota int, maxTTL time.Duration) []string {
	var v []string
	rows, err := h.q.ListInstances(ctx)
	if err != nil {
		return []string{fmt.Sprintf("ListInstances: %v", err)}
	}
	legal := map[string]bool{
		"pending": true, "starting": true, "running": true,
		"unhealthy": true, "stopped": true, "error": true, "lost": true,
	}
	portOwner := map[int32]uuid.UUID{}
	tcCount := map[string]int{}
	runningByTeam := map[uuid.UUID]int{}
	haveRow := map[uuid.UUID]bool{}
	for _, r := range rows {
		haveRow[r.ID] = true
		if !legal[r.State] {
			v = append(v, fmt.Sprintf("instance %s in illegal state %q", r.ID, r.State))
		}
		if r.HostPort != nil {
			p := *r.HostPort
			if int(p) < propPortLo || int(p) > propPortHi {
				v = append(v, fmt.Sprintf("instance %s host_port %d out of range [%d,%d]", r.ID, p, propPortLo, propPortHi))
			}
			if owner, ok := portOwner[p]; ok {
				v = append(v, fmt.Sprintf("host_port %d held by two rows: %s and %s", p, owner, r.ID))
			}
			portOwner[p] = r.ID
		}
		if r.TeamID != nil {
			key := r.TeamID.String() + "|" + r.ChallengeID.String()
			tcCount[key]++
			if tcCount[key] > 1 {
				v = append(v, fmt.Sprintf("%d instance rows for (team=%s, challenge=%s), want <=1", tcCount[key], r.TeamID, r.ChallengeID))
			}
			if r.State == "running" {
				runningByTeam[*r.TeamID]++
			}
		}
		// Extend/Start must never push a running instance's expiry beyond its max
		// lifetime (StartedAt + MaxTTL). Pending rows have no StartedAt yet — skip.
		if r.ExpiresAt != nil && r.StartedAt != nil {
			if bound := r.StartedAt.Add(maxTTL).Add(2 * time.Second); r.ExpiresAt.After(bound) {
				v = append(v, fmt.Sprintf("instance %s expires_at %v beyond StartedAt+MaxTTL %v",
					r.ID, r.ExpiresAt.UTC(), bound.UTC()))
			}
		}
	}
	for team, n := range runningByTeam {
		if n > quota {
			v = append(v, fmt.Sprintf("team %s has %d running instances, quota %d", team, n, quota))
		}
	}
	for _, cid := range h.fake.ContainerIDs() {
		if !haveRow[cid] {
			v = append(v, fmt.Sprintf("orphan container %s has no backing instance row", cid))
		}
	}
	return v
}

// drainAndVerify stops everything, reaps any leaked rows, and asserts the world is
// fully reclaimed (zero rows, zero reserved ports) — the service-level analogue of
// the soak's end-of-run teardown assertion.
func drainAndVerify(t *testing.T, ctx context.Context, h *harness, teams, chals []uuid.UUID, quota int, maxTTL time.Duration, tag string) {
	t.Helper()
	h.fake.FailDeploy = false
	h.fake.DeployFault = nil
	for _, tm := range teams {
		for _, c := range chals {
			_ = h.sched.Stop(ctx, uuid.Nil, tm, c)
		}
	}
	// Age any residual pending/error/lost rows past the reaper's wall cutoff so a
	// single ReapStaleOnce reclaims them (the reaper compares time.Now() to updated_at).
	if _, err := h.pool.Exec(ctx, `UPDATE instances SET updated_at = now() - interval '1 hour'`); err != nil {
		t.Fatalf("%s: age rows: %v", tag, err)
	}
	if _, err := h.sched.ReapStaleOnce(ctx); err != nil {
		t.Fatalf("%s: reap: %v", tag, err)
	}
	if viols := propInvariants(ctx, h, quota, maxTTL); len(viols) > 0 {
		t.Fatalf("%s: invariants violated after drain:\n  %s", tag, strings.Join(viols, "\n  "))
	}
	rows, err := h.q.ListInstances(ctx)
	if err != nil {
		t.Fatalf("%s: list: %v", tag, err)
	}
	ports, err := h.q.ListUsedPorts(ctx)
	if err != nil {
		t.Fatalf("%s: ports: %v", tag, err)
	}
	if len(rows) != 0 || len(ports) != 0 {
		t.Fatalf("%s: drain incomplete: %d instance rows and %d reserved ports remain (want 0/0)", tag, len(rows), len(ports))
	}
}

// TestSchedulerAdversarialOrderingProperty — a single-goroutine random op stream
// over a small team×challenge grid, mixing Start/Stop/Extend with clock advances,
// TTL expiry, deploy faults, and the stale-row reaper. Invariants are checked after
// EVERY step; a violation prints the seed and full op log for a deterministic replay.
func TestSchedulerAdversarialOrderingProperty(t *testing.T) {
	const (
		quota = 3
		steps = 200
	)
	maxTTL := 30 * time.Minute
	cfg := scheduler.Config{TTL: 5 * time.Minute, Extend: 5 * time.Minute, MaxTTL: maxTTL, Quota: quota, ReapAfter: time.Second}

	for _, seed := range []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			h := newHarness(t, cfg)
			ctx := context.Background()
			teams := []uuid.UUID{h.team(t), h.team(t), h.team(t)}
			chals := []uuid.UUID{
				h.challenge(t, "per_team", "static", intp(300)),
				h.challenge(t, "per_team", "static", intp(300)),
				h.challenge(t, "per_team", "static", intp(300)),
			}
			rng := rand.New(rand.NewSource(seed))
			var opLog []string

			for i := 0; i < steps; i++ {
				ti, ci := rng.Intn(len(teams)), rng.Intn(len(chals))
				team, chal := teams[ti], chals[ci]
				var op string
				// Any error from these is a legal outcome (quota, not-found, phase,
				// max-lifetime, unique-violation); the invariants must hold regardless.
				switch rng.Intn(12) {
				case 0, 1, 2, 3:
					_, created, err := h.sched.Start(ctx, uuid.Nil, team, chal)
					op = fmt.Sprintf("Start(t%d,c%d) created=%v err=%v", ti, ci, created, errShort(err))
				case 4, 5:
					err := h.sched.Stop(ctx, uuid.Nil, team, chal)
					op = fmt.Sprintf("Stop(t%d,c%d) err=%v", ti, ci, errShort(err))
				case 6, 7:
					_, err := h.sched.Extend(ctx, team, chal)
					op = fmt.Sprintf("Extend(t%d,c%d) err=%v", ti, ci, errShort(err))
				case 8:
					d := time.Duration(1+rng.Intn(10)) * time.Minute
					*h.now = h.now.Add(d)
					op = fmt.Sprintf("advanceClock(%s)", d)
				case 9:
					err := h.sched.ExpireOnce(ctx)
					op = fmt.Sprintf("ExpireOnce err=%v", errShort(err))
				case 10:
					// Flip the deploy-fault switch: subsequent Starts leave a leaked
					// error row until reaped — exercises the error-row/reap path.
					h.fake.FailDeploy = !h.fake.FailDeploy
					op = fmt.Sprintf("toggleFailDeploy=%v", h.fake.FailDeploy)
				case 11:
					// Age stale rows past the wall cutoff, then reap.
					if _, err := h.pool.Exec(ctx, `UPDATE instances SET updated_at = now() - interval '1 hour' WHERE state IN ('pending','error','lost')`); err != nil {
						t.Fatalf("age: %v", err)
					}
					n, err := h.sched.ReapStaleOnce(ctx)
					op = fmt.Sprintf("age+ReapStaleOnce=%d err=%v", n, errShort(err))
				}
				opLog = append(opLog, fmt.Sprintf("%3d: %s", i, op))

				if viols := propInvariants(ctx, h, quota, maxTTL); len(viols) > 0 {
					t.Fatalf("seed=%d step=%d: INVARIANT VIOLATED:\n  %s\n\nreplay op log:\n%s",
						seed, i, strings.Join(viols, "\n  "), strings.Join(opLog, "\n"))
				}
			}
			drainAndVerify(t, ctx, h, teams, chals, quota, maxTTL, fmt.Sprintf("seed=%d", seed))
		})
	}
}

// TestSchedulerSingleInstanceInterleavingProperty — many goroutines racing
// Start/Stop/Extend against ONE (team, challenge) while a sweeper runs
// ExpireOnce/ReapStaleOnce concurrently. Same-team ops serialize on the per-team
// lock, so this is a direct test that the lock keeps concurrent same-instance
// operations from leaking a port, orphaning a container, or duplicating the row.
// Invariants are checked at each quiescent point between concurrent bursts.
func TestSchedulerSingleInstanceInterleavingProperty(t *testing.T) {
	const (
		quota      = 3
		rounds     = 8
		goroutines = 8
		itersPer   = 25
	)
	maxTTL := 30 * time.Minute
	cfg := scheduler.Config{TTL: 5 * time.Minute, Extend: 5 * time.Minute, MaxTTL: maxTTL, Quota: quota, ReapAfter: time.Second}

	for _, seed := range []int64{1, 2, 3} {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			h := newHarness(t, cfg)
			ctx := context.Background()
			team := h.team(t)
			chal := h.challenge(t, "per_team", "static", intp(300))
			rng := rand.New(rand.NewSource(seed))

			for r := 0; r < rounds; r++ {
				var wg sync.WaitGroup
				for g := 0; g < goroutines; g++ {
					gseed := seed*1_000_000 + int64(r)*1000 + int64(g)
					wg.Add(1)
					go func() {
						defer wg.Done()
						grng := rand.New(rand.NewSource(gseed))
						for i := 0; i < itersPer; i++ {
							switch grng.Intn(3) {
							case 0:
								_, _, _ = h.sched.Start(ctx, uuid.Nil, team, chal)
							case 1:
								_ = h.sched.Stop(ctx, uuid.Nil, team, chal)
							case 2:
								_, _ = h.sched.Extend(ctx, team, chal)
							}
						}
					}()
				}
				// Concurrent sweeper: expiry + stale reap racing the hammer above.
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < itersPer; i++ {
						_ = h.sched.ExpireOnce(ctx)
						_, _ = h.sched.ReapStaleOnce(ctx)
					}
				}()
				wg.Wait() // quiescent: no goroutine touches the clock or DB now

				if viols := propInvariants(ctx, h, quota, maxTTL); len(viols) > 0 {
					t.Fatalf("seed=%d round=%d: INVARIANT VIOLATED after concurrent burst:\n  %s",
						seed, r, strings.Join(viols, "\n  "))
				}
				rows, err := h.q.ListInstances(ctx)
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(rows) > 1 {
					t.Fatalf("seed=%d round=%d: %d rows for a single (team,challenge), want <=1", seed, r, len(rows))
				}
				// Advance the clock between rounds (never during a burst) so TTLs bite.
				*h.now = h.now.Add(time.Duration(1+rng.Intn(6)) * time.Minute)
			}
			drainAndVerify(t, ctx, h, []uuid.UUID{team}, []uuid.UUID{chal}, quota, maxTTL, fmt.Sprintf("seed=%d", seed))
		})
	}
}

// errShort renders an error for the op log without dumping full wrapped chains.
func errShort(err error) string {
	if err == nil {
		return "nil"
	}
	if isConflict(err) {
		return "conflict"
	}
	s := err.Error()
	if len(s) > 40 {
		s = s[:40] + "…"
	}
	return s
}
