//go:build integration

package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/testsupport"
)

// TestPublicRoutesDoNotLeakPostFreezeSolves is the 3a-i regression: while the event is
// frozen, the public getTeam and getUser routes must NOT expose solves that landed
// after the freeze point (nor points derived from them) to non-admins — otherwise the
// frozen scoreboard can be reconstructed by polling profiles. Admins still see live.
func TestPublicRoutesDoNotLeakPostFreezeSolves(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	now := time.Now().UTC()
	freeze := now.Add(-time.Hour) // frozen an hour ago; the solve below lands "now"
	srv := fullServer(t, pool, rdb, clock.System(), now.Add(-2*time.Hour), now.Add(time.Hour), &freeze)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	_, slug := createChallenge(t, srv, admin,
		`{"title":"C","category":"misc","flag":"OSCTF{c}","scoring":"static","points_initial":100,"visible":true}`)
	alice := teamUp(t, srv, "alice", "a@example.com", "Alpha")

	// alice's team id + user id.
	var me struct {
		Id   string `json:"id"`
		Team *struct {
			Id string `json:"id"`
		} `json:"team"`
	}
	rec := do(t, srv, alice, http.MethodGet, "/api/v0/auth/me", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me.Team == nil {
		t.Fatal("alice has no team")
	}

	// Solve during the freeze (solved_at = now, after freeze_at).
	if rec := do(t, srv, alice, http.MethodPost, "/api/v0/challenges/"+slug+"/submit", `{"flag":"OSCTF{c}"}`); rec.Code != http.StatusOK {
		t.Fatalf("submit = %d", rec.Code)
	}

	teamSolves := func(jar *cookieJar) (points int, nSolves int) {
		t.Helper()
		rec := do(t, srv, jar, http.MethodGet, "/api/v0/teams/"+me.Team.Id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("getTeam = %d", rec.Code)
		}
		var td struct {
			Points int               `json:"points"`
			Solves []json.RawMessage `json:"solves"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &td)
		return td.Points, len(td.Solves)
	}
	userSolves := func(jar *cookieJar) int {
		t.Helper()
		rec := do(t, srv, jar, http.MethodGet, "/api/v0/users/"+me.Id, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("getUser = %d", rec.Code)
		}
		var pu struct {
			Solves []json.RawMessage `json:"solves"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &pu)
		return len(pu.Solves)
	}

	// A participant on a DIFFERENT team (the contrast identity).
	bob := teamUp(t, srv, "bob", "b@example.com", "Bravo")

	// Anonymous and other-team participant: the post-freeze solve is hidden.
	for name, jar := range map[string]*cookieJar{"anon": {}, "other-team": bob} {
		if pts, n := teamSolves(jar); pts != 0 || n != 0 {
			t.Errorf("%s getTeam during freeze leaked: points=%d solves=%d, want 0/0", name, pts, n)
		}
		if n := userSolves(jar); n != 0 {
			t.Errorf("%s getUser during freeze leaked %d solves, want 0", name, n)
		}
	}

	// Own team (alice, a member of Alpha) sees its own progress live during the freeze.
	if pts, n := teamSolves(alice); pts != 100 || n != 1 {
		t.Errorf("own-team getTeam = points=%d solves=%d, want 100/1 (own team sees its own)", pts, n)
	}
	if n := userSolves(alice); n != 1 {
		t.Errorf("own-team getUser = %d solves, want 1", n)
	}

	// Admin sees live.
	if pts, n := teamSolves(admin); pts != 100 || n != 1 {
		t.Errorf("admin getTeam = points=%d solves=%d, want 100/1", pts, n)
	}
	if n := userSolves(admin); n != 1 {
		t.Errorf("admin getUser = %d solves, want 1", n)
	}
}
