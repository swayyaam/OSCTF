//go:build integration

package teams_test

// Phase 5 property test for team membership — the state space the soak does not
// reach: membership changing concurrently mid-operation. Many users join/leave and
// captains leave (forcing captaincy handoff) all at once; after the churn quiesces
// the structural invariants must hold. On a violation the test stops with the seed.
// This drives the teams SERVICE directly; the same races are reachable through the
// HTTP join/leave handlers (a user clicking rapidly, captains leaving together).

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
)

func uniq(id uuid.UUID) string { return strings.ReplaceAll(id.String(), "-", "")[20:] }

// TestTeamsJoinConcurrentDoesNotExceedMaxSize is the minimized reproducer for the
// capacity race the property test surfaced: many users join ONE team at once. With
// the check-then-act Join it overran max size (observed 5+ for maxSize 4); with the
// FOR UPDATE anchor exactly maxSize slots fill. HTTP-reachable: several users
// submitting the same invite code simultaneously.
func TestTeamsJoinConcurrentDoesNotExceedMaxSize(t *testing.T) {
	const (
		maxSize   = 4
		attackers = 12
	)
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	svc := teams.New(pool, maxSize)
	ctx := context.Background()

	cap := uuid.Must(uuid.NewV7())
	if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: cap, Username: "cap" + uniq(cap), Email: uniq(cap) + "@e.test", PasswordHash: "x", Role: "user"}); err != nil {
		t.Fatalf("captain: %v", err)
	}
	tid := uuid.Must(uuid.NewV7())
	code := uniq(tid)
	if _, err := q.CreateTeam(ctx, gen.CreateTeamParams{ID: tid, Name: "t" + uniq(tid), InviteCode: code, CaptainID: cap}); err != nil {
		t.Fatalf("team: %v", err)
	}
	if err := q.AddTeamMember(ctx, gen.AddTeamMemberParams{TeamID: tid, UserID: cap}); err != nil {
		t.Fatalf("seed captain: %v", err)
	}

	joiners := make([]uuid.UUID, attackers)
	for i := range joiners {
		uid := uuid.Must(uuid.NewV7())
		if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Username: "u" + uniq(uid), Email: uniq(uid) + "@e.test", PasswordHash: "x", Role: "user"}); err != nil {
			t.Fatalf("user: %v", err)
		}
		joiners[i] = uid
	}
	var wg sync.WaitGroup
	for _, u := range joiners {
		u := u
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.Join(ctx, u, code)
		}()
	}
	wg.Wait()

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM team_members WHERE team_id=$1`, tid).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n > maxSize {
		t.Fatalf("team has %d members after a concurrent-join burst, want <=%d (max team size overrun)", n, maxSize)
	}
	if n != maxSize {
		t.Errorf("team filled to %d of %d slots — expected all slots contested and filled", n, maxSize)
	}
}

// TestTeamsConcurrentLeaveKeepsCaptainAMember is the minimized reproducer for the
// captaincy-transfer race: a team [captain A, B, C] where A and B leave at once.
// Leave read captain_id outside the tx, so the second leave decided captaincy from a
// stale value and stranded the team with a captain who was no longer a member. With
// the team locked and re-read inside the tx, the surviving team's captain is always a
// current member. HTTP-reachable: two teammates leaving simultaneously.
func TestTeamsConcurrentLeaveKeepsCaptainAMember(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	svc := teams.New(pool, 8)
	ctx := context.Background()

	mkUser := func() uuid.UUID {
		uid := uuid.Must(uuid.NewV7())
		if _, err := q.CreateUser(ctx, gen.CreateUserParams{ID: uid, Username: "u" + uniq(uid), Email: uniq(uid) + "@e.test", PasswordHash: "x", Role: "user"}); err != nil {
			t.Fatalf("user: %v", err)
		}
		return uid
	}

	for round := 0; round < 200; round++ {
		a, b, c := mkUser(), mkUser(), mkUser()
		tid := uuid.Must(uuid.NewV7())
		if _, err := q.CreateTeam(ctx, gen.CreateTeamParams{ID: tid, Name: "t" + uniq(tid), InviteCode: uniq(tid), CaptainID: a}); err != nil {
			t.Fatalf("team: %v", err)
		}
		for _, u := range []uuid.UUID{a, b, c} { // a is captain; b, c join earliest-first
			if err := q.AddTeamMember(ctx, gen.AddTeamMemberParams{TeamID: tid, UserID: u}); err != nil {
				t.Fatalf("seed member: %v", err)
			}
		}
		var wg sync.WaitGroup
		for _, u := range []uuid.UUID{a, b} { // captain A and member B leave at once
			u := u
			wg.Add(1)
			go func() { defer wg.Done(); _ = svc.Leave(ctx, u) }()
		}
		wg.Wait()

		// The team still has members (C, and maybe A or B if a leave lost a race and
		// no-op'd). Its captain must be a current member.
		var capIsMember bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM team_members m JOIN teams t ON t.id=m.team_id
			                WHERE t.id=$1 AND m.user_id=t.captain_id)`, tid).Scan(&capIsMember); err != nil {
			t.Fatalf("round=%d captain-member check: %v", round, err)
		}
		var memberCount int
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM team_members WHERE team_id=$1`, tid).Scan(&memberCount)
		if memberCount > 0 && !capIsMember {
			var cap uuid.UUID
			_ = pool.QueryRow(ctx, `SELECT captain_id FROM teams WHERE id=$1`, tid).Scan(&cap)
			t.Fatalf("round=%d: team %s has %d members but captain %s is not among them", round, tid, memberCount, cap)
		}
	}
}

// TestTeamsMembershipIntegrityUnderConcurrencyProperty hammers Join/Leave (and
// captain departures) concurrently, then asserts:
//   - every user is on at most one team (uq_team_members_user — no torn state);
//   - no team exceeds maxSize;
//   - every surviving non-empty team's captain is a current member of that team
//     (Leave must transfer captaincy to a remaining member — the one invariant not
//     enforced by a constraint, so the interesting target).
func TestTeamsMembershipIntegrityUnderConcurrencyProperty(t *testing.T) {
	const (
		maxSize  = 4
		numUsers = 20
		numTeams = 6
	)
	for _, seed := range []int64{1, 2, 3, 4, 5, 6, 7, 8} {
		seed := seed
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			pool, _ := testsupport.Postgres(t)
			q := gen.New(pool)
			svc := teams.New(pool, maxSize)
			ctx := context.Background()

			users := make([]uuid.UUID, numUsers)
			for i := range users {
				uid := uuid.Must(uuid.NewV7())
				if _, err := q.CreateUser(ctx, gen.CreateUserParams{
					ID: uid, Username: "u" + uniq(uid), Email: uniq(uid) + "@e.test", PasswordHash: "x", Role: "user",
				}); err != nil {
					t.Fatalf("create user: %v", err)
				}
				users[i] = uid
			}
			// The first numTeams users each captain a team, seeded as its sole member.
			codes := make([]string, numTeams)
			for i := 0; i < numTeams; i++ {
				cap := users[i]
				tid := uuid.Must(uuid.NewV7())
				code := uniq(tid)
				if _, err := q.CreateTeam(ctx, gen.CreateTeamParams{
					ID: tid, Name: "t" + uniq(tid), InviteCode: code, CaptainID: cap,
				}); err != nil {
					t.Fatalf("create team: %v", err)
				}
				if err := q.AddTeamMember(ctx, gen.AddTeamMemberParams{TeamID: tid, UserID: cap}); err != nil {
					t.Fatalf("seed captain member: %v", err)
				}
				codes[i] = code
			}

			var wg sync.WaitGroup
			launch := func(u uuid.UUID, gseed int64, iters, leaveWeight int) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					grng := rand.New(rand.NewSource(gseed))
					for i := 0; i < iters; i++ {
						if grng.Intn(leaveWeight) == 0 {
							_ = svc.Leave(ctx, u)
						} else {
							_, _ = svc.Join(ctx, u, codes[grng.Intn(numTeams)])
						}
					}
				}()
			}
			// Mobile users churn join/leave; captains leave more often to force handoffs.
			for i := numTeams; i < numUsers; i++ {
				launch(users[i], seed*1000+int64(i), 30, 2)
			}
			for i := 0; i < numTeams; i++ {
				launch(users[i], seed*7000+int64(i), 15, 3) // leave 1-in-3 → captaincy transfer
			}
			wg.Wait()

			// (1) At most one team per user.
			var dupUser uuid.UUID
			var dupN int
			if err := pool.QueryRow(ctx,
				`SELECT user_id, count(*) FROM team_members GROUP BY user_id ORDER BY count(*) DESC LIMIT 1`).
				Scan(&dupUser, &dupN); err == nil && dupN > 1 {
				t.Fatalf("seed=%d: user %s is on %d teams, want <=1", seed, dupUser, dupN)
			}
			// (2) No team over capacity.
			var bigTeam uuid.UUID
			var bigN int
			if err := pool.QueryRow(ctx,
				`SELECT team_id, count(*) FROM team_members GROUP BY team_id ORDER BY count(*) DESC LIMIT 1`).
				Scan(&bigTeam, &bigN); err == nil && bigN > maxSize {
				t.Fatalf("seed=%d: team %s has %d members, want <=%d", seed, bigTeam, bigN, maxSize)
			}
			// (3) Every surviving non-empty team's captain is a current member.
			rows, err := pool.Query(ctx, `
				SELECT t.id, t.captain_id
				FROM teams t
				WHERE EXISTS (SELECT 1 FROM team_members m WHERE m.team_id = t.id)
				  AND NOT EXISTS (SELECT 1 FROM team_members m WHERE m.team_id = t.id AND m.user_id = t.captain_id)`)
			if err != nil {
				t.Fatalf("captain-integrity query: %v", err)
			}
			defer rows.Close()
			var orphaned []string
			for rows.Next() {
				var tid, cap uuid.UUID
				if err := rows.Scan(&tid, &cap); err != nil {
					t.Fatalf("scan: %v", err)
				}
				orphaned = append(orphaned, fmt.Sprintf("team %s captain %s not a member", tid, cap))
			}
			if len(orphaned) > 0 {
				t.Fatalf("seed=%d: %d team(s) with a captain who is not a member:\n  %s",
					seed, len(orphaned), strings.Join(orphaned, "\n  "))
			}
		})
	}
}
