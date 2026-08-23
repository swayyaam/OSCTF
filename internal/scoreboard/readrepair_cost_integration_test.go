//go:build integration

package scoreboard

// Read-repair adds a CountValidSolves query to every Current() call (the hottest read
// path, amplified by WS). This measures its cost against the full ListValidSolves read and
// a whole compute(), at a realistic large-event scale, so the "a count is cheap" claim is
// confirmed rather than assumed. Run: go test -tags integration -run CountCost -v ./internal/scoreboard

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/swayyaam/OSCTF/internal/clock"
	"github.com/swayyaam/OSCTF/internal/db/gen"
	"github.com/swayyaam/OSCTF/internal/testsupport"
)

func TestReadRepairCountCost(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	ctx := context.Background()

	const teams, challs = 200, 60 // 12,000 solves — a large event
	if _, err := q.CreateEvent(ctx, gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: time.Now().Add(-time.Hour), EndsAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("event: %v", err)
	}
	// Bulk-seed users+teams+members and challenges, then every (team,challenge) as a solve.
	for i := 0; i < teams; i++ {
		s := fmt.Sprintf("%d", i)
		if _, err := pool.Exec(ctx, `
			WITH u AS (INSERT INTO users (id, username, email, password_hash, role)
			           VALUES (gen_random_uuid(), 'user'||$1, 'user'||$1||'@e.test', 'x', 'user') RETURNING id),
			     t AS (INSERT INTO teams (id, name, invite_code, captain_id)
			           SELECT gen_random_uuid(), 'team'||$1, 'code'||$1, u.id FROM u RETURNING id, captain_id)
			INSERT INTO team_members (team_id, user_id) SELECT t.id, t.captain_id FROM t`, s); err != nil {
			t.Fatalf("seed team %d: %v", i, err)
		}
	}
	for j := 0; j < challs; j++ {
		s := fmt.Sprintf("%d", j)
		if _, err := pool.Exec(ctx, `INSERT INTO challenges
			(id, slug, title, category, kind, flag, scoring, points_initial, mem_limit_mb, cpu_millis, container_env, visible)
			VALUES (gen_random_uuid(), 'chal'||$1, 'C', 'web', 'standard', 'OSCTF{f}', 'static', 500, 128, 500, '{}', true)`, s); err != nil {
			t.Fatalf("seed challenge %d: %v", j, err)
		}
	}
	// Every team solves every challenge (12k rows) in one statement.
	if _, err := pool.Exec(ctx, `
		INSERT INTO submissions (id, challenge_id, team_id, user_id, provided, correct)
		SELECT gen_random_uuid(), c.id, tm.team_id, tm.user_id, 'OSCTF{f}', true
		FROM challenges c CROSS JOIN team_members tm`); err != nil {
		t.Fatalf("seed solves: %v", err)
	}

	n, err := q.CountValidSolves(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	t.Logf("scale: %d valid solves", n)

	bench := func(name string, iters int, fn func()) {
		fn() // warm
		start := time.Now()
		for i := 0; i < iters; i++ {
			fn()
		}
		per := time.Since(start) / time.Duration(iters)
		t.Logf("%-22s %v/op", name, per.Round(time.Microsecond))
	}
	bench("CountValidSolves", 200, func() { _, _ = q.CountValidSolves(ctx) })
	bench("ListValidSolves", 50, func() { _, _ = q.ListValidSolves(ctx) })
	clk := clock.System()
	bench("compute (full)", 20, func() { _, _, _ = compute(ctx, q, clk()) })
}
