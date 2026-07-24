package handlers_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/osctf/platform/internal/testsupport"
)

// registerUser registers a user and returns a cookie jar bound to the session.
func registerUser(t *testing.T, srv http.Handler, username, email string) *cookieJar {
	t.Helper()
	jar := &cookieJar{}
	rec := do(t, srv, jar, http.MethodPost, "/api/v0/auth/register",
		`{"username":"`+username+`","email":"`+email+`","password":"supersecret1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register %s = %d (%s)", username, rec.Code, rec.Body)
	}
	return jar
}

func TestTeamLifecycleIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := newTestServer(t, pool, rdb)

	cap := registerUser(t, srv, "captain", "cap@example.com")
	m1 := registerUser(t, srv, "member1", "m1@example.com")
	m2 := registerUser(t, srv, "member2", "m2@example.com")

	// Captain creates a team; response carries the invite code.
	rec := do(t, srv, cap, http.MethodPost, "/api/v0/teams", `{"name":"Hackers"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create team = %d (%s)", rec.Code, rec.Body)
	}
	var team struct {
		ID         string `json:"id"`
		InviteCode string `json:"invite_code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &team)
	if team.InviteCode == "" {
		t.Fatal("no invite code returned")
	}

	// Duplicate name → 409.
	cap2 := registerUser(t, srv, "other", "other@example.com")
	if rec := do(t, srv, cap2, http.MethodPost, "/api/v0/teams", `{"name":"Hackers"}`); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate team name = %d, want 409", rec.Code)
	}

	// Two members join.
	for _, j := range []*cookieJar{m1, m2} {
		if rec := do(t, srv, j, http.MethodPost, "/api/v0/teams/join", `{"invite_code":"`+team.InviteCode+`"}`); rec.Code != http.StatusOK {
			t.Fatalf("join = %d (%s)", rec.Code, rec.Body)
		}
	}

	// Third joiner exceeds the default max size (4) — captain + m1 + m2 + cap2? No,
	// cap2 has their own team. Add two more to hit the cap.
	m3 := registerUser(t, srv, "member3", "m3@example.com")
	if rec := do(t, srv, m3, http.MethodPost, "/api/v0/teams/join", `{"invite_code":"`+team.InviteCode+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("4th member join = %d, want 200", rec.Code)
	}
	m4 := registerUser(t, srv, "member4", "m4@example.com")
	if rec := do(t, srv, m4, http.MethodPost, "/api/v0/teams/join", `{"invite_code":"`+team.InviteCode+`"}`); rec.Code != http.StatusConflict {
		t.Fatalf("5th member join = %d, want 409 (team full)", rec.Code)
	}

	// Captain leaves → captaincy transfers to the earliest joiner (member1).
	if rec := do(t, srv, cap, http.MethodPost, "/api/v0/teams/leave", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("captain leave = %d", rec.Code)
	}
	rec = do(t, srv, m1, http.MethodGet, "/api/v0/teams/"+team.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get team = %d", rec.Code)
	}
	// member1 (new captain) sees the invite code; a random viewer does not.
	var detail struct {
		Members    []struct{ Username string } `json:"members"`
		InviteCode *string                     `json:"invite_code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if detail.InviteCode == nil {
		t.Error("new captain should see the invite code")
	}
	// captain + member1 + member2 + member3 = 4 joined; captain left → 3 remain.
	if len(detail.Members) != 3 {
		t.Errorf("team has %d members, want 3", len(detail.Members))
	}

	// A non-member must not see the invite code.
	rec = do(t, srv, cap2, http.MethodGet, "/api/v0/teams/"+team.ID, "")
	var asOther struct {
		InviteCode *string `json:"invite_code"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &asOther)
	if asOther.InviteCode != nil {
		t.Error("non-member should not see the invite code")
	}
}
