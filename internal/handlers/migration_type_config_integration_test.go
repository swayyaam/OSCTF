//go:build integration

package handlers_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/swayyaam/OSCTF/internal/testsupport"
)

// TestTypeConfigMigrationNoOp verifies migration 0008 (add `type_config`) is a no-op for existing
// rows: on a schema without it, every challenge lands on '{}' and no other column changes — the
// same treatment 0003/0004/0005 got. It runs the EXACT shipped Up SQL via gooseUp, not a copy.
func TestTypeConfigMigrationNoOp(t *testing.T) {
	pool, _ := testsupport.Postgres(t) // 0008 already applied by testsupport
	ctx := context.Background()

	// Simulate the pre-0008 schema by dropping the column 0008 adds.
	if _, err := pool.Exec(ctx, `ALTER TABLE challenges DROP COLUMN type_config`); err != nil {
		t.Fatalf("drop type_config to simulate the pre-0008 schema: %v", err)
	}

	// Seed pre-0008 rows (which already carry `type` from 0005): a static challenge and a dynamic
	// container one, so distinct values across many columns are exercised.
	if _, err := pool.Exec(ctx, `INSERT INTO challenges
		(id, slug, title, category, kind, flag, scoring, points_initial, visible, type)
		VALUES ($1, 's1', 'Static One', 'web', 'standard', 'OSCTF{s1}', 'static', 100, true, 'standard')`,
		uuid.Must(uuid.NewV7())); err != nil {
		t.Fatalf("seed static: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO challenges
		(id, slug, title, category, kind, flag, scoring, points_initial, points_min, decay,
		 visible, image, internal_port, instancing, flag_mode, type)
		VALUES ($1, 'c1', 'Container One', 'pwn', 'container', 'OSCTF{c1}', 'dynamic', 500, 100, 10,
		 true, 'osctf/x:1', 8000, 'per_team', 'static', 'standard')`,
		uuid.Must(uuid.NewV7())); err != nil {
		t.Fatalf("seed container: %v", err)
	}

	// Full row state EXCLUDING type_config — the "nothing else changed" baseline. to_jsonb(row) -
	// 'type_config' is a no-op before the migration and drops the new column after, so the two dumps
	// are comparable.
	dump := func() string {
		var s string
		if err := pool.QueryRow(ctx,
			`SELECT coalesce(jsonb_agg(to_jsonb(c) - 'type_config' ORDER BY c.id)::text, '[]') FROM challenges c`).Scan(&s); err != nil {
			t.Fatalf("dump: %v", err)
		}
		return s
	}
	before := dump()

	// Run the EXACT shipped 0008 Up.
	if _, err := pool.Exec(ctx, gooseUp(t, "../db/migrations/0008_challenge_type_config.sql")); err != nil {
		t.Fatalf("run migration 0008: %v", err)
	}

	var total, nonEmpty int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM challenges`).Scan(&total)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM challenges WHERE type_config <> '{}'::jsonb`).Scan(&nonEmpty)
	if total == 0 {
		t.Fatal("no seeded rows — the test exercised nothing")
	}
	if nonEmpty != 0 {
		t.Errorf("%d of %d rows did not default to type_config='{}' after 0008", nonEmpty, total)
	}
	if after := dump(); after != before {
		t.Fatalf("0008 changed a non-type_config column:\n before = %s\n after  = %s", before, after)
	}
	t.Logf("migration 0008 no-op: %d pre-0008 rows all defaulted to type_config='{}'; no other column changed", total)
}
