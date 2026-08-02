//go:build integration

package handlers_test

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/testsupport"
)

// gooseUp returns the "+goose Up" statements of a migration file, so the test runs the
// EXACT SQL that ships, not a copy that could drift.
func gooseUp(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read migration %s: %v", path, err)
	}
	s := string(b)
	up := s[strings.Index(s, "+goose Up")+len("+goose Up"):]
	if i := strings.Index(up, "+goose Down"); i >= 0 {
		up = up[:i]
	}
	return up
}

// TestHistoricalProvidedRedactionMigration verifies migration 0004 backfills the same
// rows the v0.2.1 write-time redaction would, and reports how many rows it touches on a
// seeded database. Historical rows are inserted directly (as v0.2 stored them, verbatim),
// since the live code now redacts on write.
func TestHistoricalProvidedRedactionMigration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)
	ctx := context.Background()

	admin := makeAdmin(t, srv, pool, "root", "root@x.test")
	var adminUID string
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email='root@x.test'`).Scan(&adminUID); err != nil {
		t.Fatalf("admin id: %v", err)
	}
	const staticFlag = "OSCTF{static}"
	statID, _ := createChallenge(t, srv, admin, staticChallengeBody("Stat", staticFlag))
	instID, instSlug := createChallenge(t, srv, admin, perInstanceBody("Inst"))

	// Three teams each start an instance → three real per-instance flags.
	teamFlag := func(name string) (teamID, flag string) {
		jar := teamUp(t, srv, name, name+"@x.test", "T"+name)
		if rec := do(t, srv, jar, http.MethodPost, "/api/v0/challenges/"+instSlug+"/instance", ""); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
			t.Fatalf("%s start = %d (%s)", name, rec.Code, rec.Body)
		}
		_, teamID = meOf(t, srv, jar)
		if err := pool.QueryRow(ctx, `SELECT flag FROM instances WHERE challenge_id=$1 AND team_id=$2`, instID, teamID).Scan(&flag); err != nil {
			t.Fatalf("read %s flag: %v", name, err)
		}
		return teamID, flag
	}
	teamA, flagA := teamFlag("alpha")
	teamB, flagB := teamFlag("bravo")
	teamC, flagC := teamFlag("charlie")

	ins := func(chal, team, provided string, correct bool) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO submissions (id, challenge_id, team_id, user_id, provided, correct) VALUES ($1,$2,$3,$4,$5,$6)`,
			uuid.Must(uuid.NewV7()), chal, team, adminUID, provided, correct); err != nil {
			t.Fatalf("insert submission: %v", err)
		}
	}
	// Rows that SHOULD be redacted:
	ins(instID, teamA, flagA, true)  // A's own correct solve → provided is A's flag
	ins(instID, teamB, flagA, false) // B submitted A's flag (sharing); A's instance is live
	ins(instID, teamB, flagB, true)  // B's own correct solve
	// Rows that should be LEFT intact:
	ins(instID, teamA, "totally-wrong-guess", false) // a genuine wrong guess
	ins(instID, teamA, flagC, false)                 // shared C's flag, but C's instance is destroyed below
	ins(statID, teamA, staticFlag, true)             // static challenge — provided is not a per-instance secret
	// Destroy C's instance so its flag can no longer be matched by value (the documented
	// best-effort limitation, shared with the runtime detector).
	if _, err := pool.Exec(ctx, `DELETE FROM instances WHERE challenge_id=$1 AND team_id=$2`, instID, teamC); err != nil {
		t.Fatalf("destroy C instance: %v", err)
	}

	var total int
	pool.QueryRow(ctx, `SELECT count(*) FROM submissions`).Scan(&total)

	// Run the real migration 0004 SQL.
	if _, err := pool.Exec(ctx, gooseUp(t, "../db/migrations/0004_redact_historical_provided_flags.sql")); err != nil {
		t.Fatalf("run migration 0004: %v", err)
	}

	const redacted = "[redacted per-instance flag]"
	var got int
	pool.QueryRow(ctx, `SELECT count(*) FROM submissions WHERE provided=$1`, redacted).Scan(&got)
	if got != 3 {
		t.Errorf("redacted %d rows, want 3", got)
	}

	// Nothing that must survive was touched, and no real flag remains anywhere.
	for _, keep := range []string{"totally-wrong-guess", flagC, staticFlag} {
		var n int
		pool.QueryRow(ctx, `SELECT count(*) FROM submissions WHERE provided=$1`, keep).Scan(&n)
		if n != 1 {
			t.Errorf("expected the %q submission to survive, found %d", keep, n)
		}
	}
	for _, gone := range []string{flagA, flagB} {
		var n int
		pool.QueryRow(ctx, `SELECT count(*) FROM submissions WHERE provided=$1`, gone).Scan(&n)
		if n != 0 {
			t.Errorf("real per-instance flag still in submissions.provided after migration (%d rows)", n)
		}
	}
	_ = flagC
	t.Logf("historical redaction: %d of %d submission rows redacted on the seeded DB (3 per-instance secrets: 2 correct solves + 1 live-instance sharing; 3 left intact: wrong guess, destroyed-instance share, static solve)", got, total)
}
