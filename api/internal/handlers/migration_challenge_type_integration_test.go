//go:build integration

package handlers_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/testsupport"
)

// TestChallengeTypeMigrationNoOp verifies migration 0005 (add `type`) is a no-op for
// existing rows: on a v0.2-shaped schema, every challenge lands on the built-in 'standard'
// type and no other column changes — the same treatment 0003/0004 got. It runs the EXACT
// shipped Up SQL via gooseUp, not a copy.
func TestChallengeTypeMigrationNoOp(t *testing.T) {
	pool, _ := testsupport.Postgres(t) // 0005 already applied by testsupport
	ctx := context.Background()

	// Simulate the pre-0005 (v0.2) schema by dropping the column 0005 adds.
	if _, err := pool.Exec(ctx, `ALTER TABLE challenges DROP COLUMN type`); err != nil {
		t.Fatalf("drop type to simulate the v0.2 schema: %v", err)
	}

	// Seed v0.2-shaped rows directly (no `type`): a static challenge and a dynamic container
	// one, so distinct values across many columns are exercised.
	if _, err := pool.Exec(ctx, `INSERT INTO challenges
		(id, slug, title, category, kind, flag, scoring, points_initial, visible)
		VALUES ($1, 's1', 'Static One', 'web', 'standard', 'OSCTF{s1}', 'static', 100, true)`,
		uuid.Must(uuid.NewV7())); err != nil {
		t.Fatalf("seed static: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO challenges
		(id, slug, title, category, kind, flag, scoring, points_initial, points_min, decay,
		 visible, image, internal_port, instancing, flag_mode)
		VALUES ($1, 'c1', 'Container One', 'pwn', 'container', 'OSCTF{c1}', 'dynamic', 500, 100, 10,
		 true, 'osctf/x:1', 8000, 'per_team', 'static')`,
		uuid.Must(uuid.NewV7())); err != nil {
		t.Fatalf("seed container: %v", err)
	}

	// Full row state EXCLUDING type (which doesn't exist yet), ordered — the "nothing else
	// changed" baseline. to_jsonb(row) - 'type' is a no-op here and drops the new column
	// after the migration, so the two dumps are comparable.
	dump := func() string {
		var s string
		if err := pool.QueryRow(ctx,
			`SELECT coalesce(jsonb_agg(to_jsonb(c) - 'type' ORDER BY c.id)::text, '[]') FROM challenges c`).Scan(&s); err != nil {
			t.Fatalf("dump: %v", err)
		}
		return s
	}
	before := dump()

	// Run the EXACT shipped 0005 Up.
	if _, err := pool.Exec(ctx, gooseUp(t, "../db/migrations/0005_challenge_type.sql")); err != nil {
		t.Fatalf("run migration 0005: %v", err)
	}

	var total, nonStandard int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM challenges`).Scan(&total)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM challenges WHERE type <> 'standard'`).Scan(&nonStandard)
	if total == 0 {
		t.Fatal("no seeded rows — the test exercised nothing")
	}
	if nonStandard != 0 {
		t.Errorf("%d of %d rows did not default to type='standard' after 0005", nonStandard, total)
	}
	if after := dump(); after != before {
		t.Fatalf("0005 changed a non-type column:\n before = %s\n after  = %s", before, after)
	}
	t.Logf("migration 0005 no-op: %d v0.2-shaped rows all defaulted to type='standard'; no other column changed", total)
}
