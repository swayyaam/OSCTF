package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/testsupport"
)

// makeAdmin registers a user then promotes them to admin in the DB, returning a
// logged-in cookie jar. Promotion happens before login so the session role is admin.
func makeAdmin(t *testing.T, srv http.Handler, pool *pgxpool.Pool, username, email string) *cookieJar {
	t.Helper()
	registerUser(t, srv, username, email) // creates the row
	q := gen.New(pool)
	u, err := q.GetUserByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("lookup admin: %v", err)
	}
	role := "admin"
	if _, err := q.UpdateUserAdminFields(context.Background(), gen.UpdateUserAdminFieldsParams{ID: u.ID, Role: &role}); err != nil {
		t.Fatalf("promote admin: %v", err)
	}
	jar := &cookieJar{}
	rec := do(t, srv, jar, http.MethodPost, "/api/v0/auth/login",
		`{"email":"`+email+`","password":"supersecret1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin login = %d", rec.Code)
	}
	return jar
}

func TestAdminBanRevokesSessionsIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := newTestServer(t, pool, rdb)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	target := registerUser(t, srv, "victim", "victim@example.com")

	// Target has a working session.
	if rec := do(t, srv, target, http.MethodGet, "/api/v0/auth/me", ""); rec.Code != http.StatusOK {
		t.Fatalf("target me = %d, want 200", rec.Code)
	}

	// Find the target's ID via the admin user list.
	rec := do(t, srv, admin, http.MethodGet, "/api/v0/admin/users?q=victim", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list users = %d (%s)", rec.Code, rec.Body)
	}
	var page struct {
		Items []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) == 0 {
		t.Fatal("victim not found in admin list")
	}
	victimID := page.Items[0].ID

	// Admin bans the target.
	if rec := do(t, srv, admin, http.MethodPatch, "/api/v0/admin/users/"+victimID, `{"banned":true}`); rec.Code != http.StatusOK {
		t.Fatalf("ban = %d (%s)", rec.Code, rec.Body)
	}

	// The target's existing session is now revoked → 401.
	if rec := do(t, srv, target, http.MethodGet, "/api/v0/auth/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("banned target me = %d, want 401", rec.Code)
	}

	// Admin cannot ban themselves → 409.
	adminID := adminUserID(t, pool, "root@example.com")
	if rec := do(t, srv, admin, http.MethodPatch, "/api/v0/admin/users/"+adminID, `{"banned":true}`); rec.Code != http.StatusConflict {
		t.Fatalf("self-ban = %d, want 409", rec.Code)
	}
}

func TestListTeamsExcludesHiddenIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := newTestServer(t, pool, rdb)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	u := registerUser(t, srv, "player", "player@example.com")
	rec := do(t, srv, u, http.MethodPost, "/api/v0/teams", `{"name":"Visible Team"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create team = %d", rec.Code)
	}
	var team struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &team)

	// Public list shows it.
	if !teamListed(t, srv, "Visible Team") {
		t.Fatal("visible team missing from public list")
	}

	// Admin hides it → gone from public list.
	if rec := do(t, srv, admin, http.MethodPatch, "/api/v0/admin/teams/"+team.ID, `{"hidden":true}`); rec.Code != http.StatusOK {
		t.Fatalf("hide team = %d (%s)", rec.Code, rec.Body)
	}
	if teamListed(t, srv, "Visible Team") {
		t.Fatal("hidden team still in public list")
	}
}

func teamListed(t *testing.T, srv http.Handler, name string) bool {
	t.Helper()
	rec := do(t, srv, &cookieJar{}, http.MethodGet, "/api/v0/teams", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list teams = %d", rec.Code)
	}
	var teams []struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &teams)
	for _, tm := range teams {
		if tm.Name == name {
			return true
		}
	}
	return false
}

func adminUserID(t *testing.T, pool *pgxpool.Pool, email string) string {
	t.Helper()
	u, err := gen.New(pool).GetUserByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("lookup %s: %v", email, err)
	}
	return u.ID.String()
}
