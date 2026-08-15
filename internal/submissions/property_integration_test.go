//go:build integration

package submissions_test

// Phase 5 property tests for the submission path — the state space the soak does
// not reach: many submissions racing each other and racing the event-phase
// transition. Invariants are checked after the concurrent burst quiesces. On a
// violation the test stops with the seed for replay. These drive the submissions
// SERVICE directly; both invariants below are reachable through the HTTP submit
// handler (teammates racing the same flag; a buzzer-beater at event end).

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/submissions"
	"github.com/osctf/platform/internal/testsupport"
)

// atomicClock is a race-free mutable clock: many goroutines read now() while one
// advances it, modelling the event-phase transition happening mid-submission.
type atomicClock struct {
	base time.Time
	off  atomic.Int64 // extra nanoseconds
}

func (c *atomicClock) now() time.Time          { return c.base.Add(time.Duration(c.off.Load())) }
func (c *atomicClock) advance(d time.Duration) { c.off.Add(int64(d)) }

type subHarness struct {
	pool *pgxpool.Pool
	q    *gen.Queries
	svc  *submissions.Service
	clk  *atomicClock
	slug string
	flag string
	chID uuid.UUID
}

// newSubHarness stands up the submissions service on a mutable clock with a static
// challenge (known flag) and an event window [t0-1h, t0+1h].
func newSubHarness(t *testing.T) *subHarness {
	t.Helper()
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &atomicClock{base: t0}
	var clkFn clock.Clock = clk.now
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: t0.Add(-time.Hour), EndsAt: t0.Add(time.Hour),
	}); err != nil {
		t.Fatalf("create event: %v", err)
	}
	svc := submissions.New(pool, events.New(q, clkFn), clkFn, audit.New(q, testsupport.DiscardLogger()))
	id := uuid.Must(uuid.NewV7())
	slug := "prop-" + uniq(id)
	flag := "OSCTF{prop_" + uniq(id) + "}"
	img := "x/y:latest"
	port := int32(8000)
	if _, err := q.CreateChallenge(context.Background(), gen.CreateChallengeParams{
		ID: id, Slug: slug, Title: "P", Category: "web", Kind: "container",
		Flag: flag, Scoring: "static", PointsInitial: 100,
		Image: &img, InternalPort: &port, MemLimitMb: 128, CpuMillis: 500,
		ContainerEnv: []byte("{}"), Visible: true,
	}); err != nil {
		t.Fatalf("create challenge: %v", err)
	}
	return &subHarness{pool: pool, q: q, svc: svc, clk: clk, slug: slug, flag: flag, chID: id}
}

func (h *subHarness) team(t *testing.T) (teamID, userID uuid.UUID) {
	t.Helper()
	uid := uuid.Must(uuid.NewV7())
	if _, err := h.q.CreateUser(context.Background(), gen.CreateUserParams{
		ID: uid, Username: "u" + uniq(uid), Email: uniq(uid) + "@e.test", PasswordHash: "x", Role: "user",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	tid := uuid.Must(uuid.NewV7())
	if _, err := h.q.CreateTeam(context.Background(), gen.CreateTeamParams{
		ID: tid, Name: "t" + uniq(tid), InviteCode: uniq(tid), CaptainID: uid,
	}); err != nil {
		t.Fatalf("create team: %v", err)
	}
	return tid, uid
}

func (h *subHarness) solveCount(ctx context.Context, teamID uuid.UUID) int {
	var n int
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM submissions WHERE team_id=$1 AND challenge_id=$2 AND correct`,
		teamID, h.chID).Scan(&n); err != nil {
		return -1
	}
	return n
}

// TestSubmissionsConcurrentSolveNoDoubleCountProperty — many goroutines submit the
// correct flag for the same challenge at once, per team. Exactly one valid solve
// must be recorded per team: the challenge row-lock + HasTeamSolved must serialize
// the winner. A double count would corrupt the dynamic solve-count denominator.
func TestSubmissionsConcurrentSolveNoDoubleCountProperty(t *testing.T) {
	const (
		numTeams = 6
		racers   = 8
	)
	for round := 1; round <= 5; round++ {
		round := round
		t.Run(fmt.Sprintf("round=%d", round), func(t *testing.T) {
			h := newSubHarness(t)
			ctx := context.Background()
			type tu struct{ tid, uid uuid.UUID }
			teams := make([]tu, numTeams)
			for i := range teams {
				teams[i].tid, teams[i].uid = h.team(t)
			}
			var wg sync.WaitGroup
			for _, tm := range teams {
				tm := tm
				for g := 0; g < racers; g++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, _ = h.svc.Submit(ctx, submissions.Input{
							UserID: tm.uid, TeamID: tm.tid, Slug: h.slug, Flag: h.flag,
						})
					}()
				}
			}
			wg.Wait()
			total := 0
			for _, tm := range teams {
				n := h.solveCount(ctx, tm.tid)
				if n != 1 {
					t.Fatalf("round=%d team %s recorded %d correct solves for one challenge, want exactly 1 (double-solve under concurrency)", round, tm.tid, n)
				}
				total += n
			}
			if total != numTeams {
				t.Fatalf("round=%d total solves=%d, want %d", round, total, numTeams)
			}
		})
	}
}

// TestSubmissionsRacingEventEndProperty — submissions fire while the clock crosses
// ends_at (running→ended). After the race: no team double-solved, and once the
// event is firmly ended a fresh submit is rejected with no solve recorded. The
// phase gate is check-then-act outside the tx, so a submit that observed "running"
// microseconds before the crossing may still commit — that TOCTOU is acceptable
// (buzzer-beater) and is NOT asserted; integrity and the post-end gate are.
func TestSubmissionsRacingEventEndProperty(t *testing.T) {
	const numTeams = 10
	for _, seed := range []int64{1, 2, 3, 4, 5} {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			h := newSubHarness(t)
			ctx := context.Background()
			type tu struct{ tid, uid uuid.UUID }
			teams := make([]tu, numTeams)
			for i := range teams {
				teams[i].tid, teams[i].uid = h.team(t)
			}
			rng := rand.New(rand.NewSource(seed))
			var wg sync.WaitGroup
			for _, tm := range teams {
				tm := tm
				delay := time.Duration(rng.Intn(3000)) * time.Microsecond
				wg.Add(1)
				go func() {
					defer wg.Done()
					time.Sleep(delay)
					_, _ = h.svc.Submit(ctx, submissions.Input{
						UserID: tm.uid, TeamID: tm.tid, Slug: h.slug, Flag: h.flag,
					})
				}()
			}
			// Racer: cross ends_at (t0+1h) partway through the submission burst.
			jitter := time.Duration(rng.Intn(2000)) * time.Microsecond
			wg.Add(1)
			go func() {
				defer wg.Done()
				time.Sleep(jitter)
				h.clk.advance(2 * time.Hour) // now t0+2h > ends_at → ended
			}()
			wg.Wait()

			// (1) No team double-solved across the transition.
			for _, tm := range teams {
				if n := h.solveCount(ctx, tm.tid); n > 1 {
					t.Fatalf("seed=%d team %s has %d solves, want <=1", seed, tm.tid, n)
				}
			}
			// (2) Event is firmly ended: a fresh submit is rejected and records nothing.
			extraT, extraU := h.team(t)
			if _, err := h.svc.Submit(ctx, submissions.Input{
				UserID: extraU, TeamID: extraT, Slug: h.slug, Flag: h.flag,
			}); err == nil {
				t.Fatalf("seed=%d: submit accepted after the event ended, want rejection", seed)
			}
			if n := h.solveCount(ctx, extraT); n != 0 {
				t.Fatalf("seed=%d: %d solve(s) recorded after the event ended, want 0", seed, n)
			}
		})
	}
}
