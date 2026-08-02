//go:build integration

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
)

// TestInstanceLifecycleWithFakeRuntime exercises the deploy/status/restart/destroy
// endpoints and connection-info rendering without Docker.
func TestInstanceLifecycleWithFakeRuntime(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	q := gen.New(pool)
	now := time.Now().UTC()
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("event: %v", err)
	}
	sessions := auth.NewSessionStore(rdb, time.Hour)
	fake := runtime.NewFakeRuntime(q)
	mgr := runtime.NewManager(fake, q, "ctf.example.com", 30000, 30999)
	h := handlers.New(handlers.Deps{
		Users: users.New(q, sessions, true), Teams: teams.New(pool, 4),
		Events: events.New(q, clock.System()), Challenges: challenges.New(q, newMemStore()),
		Runtime: mgr, Auth: auth.NewEmailPasswordProvider(q, nil), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, discardLog()), SessionTTL: time.Hour,
	})
	srv := httpserver.New(httpserver.Deps{Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin})

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	id, slug := createChallenge(t, srv, admin, `{
		"title":"Web","category":"web","kind":"container","flag":"OSCTF{c}",
		"image":"osctf/example:0.1","internal_port":8000,
		"connection_template":"http://{host}:{port}","scoring":"static","points_initial":100,"visible":true
	}`)

	// Deploy → running, with connection info rendered.
	rec := do(t, srv, admin, http.MethodPost, "/api/v0/admin/challenges/"+id+"/instance", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy = %d (%s)", rec.Code, rec.Body)
	}
	var inst struct {
		State          string  `json:"state"`
		HostPort       *int    `json:"host_port"`
		ConnectionInfo *string `json:"connection_info"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &inst)
	if inst.State != "running" {
		t.Fatalf("state = %q, want running", inst.State)
	}
	if inst.HostPort == nil || *inst.HostPort != 30000 {
		t.Fatalf("host_port = %v, want 30000", inst.HostPort)
	}
	if inst.ConnectionInfo == nil || *inst.ConnectionInfo != "http://ctf.example.com:30000" {
		t.Fatalf("connection_info = %v", inst.ConnectionInfo)
	}

	// Deploy again is idempotent (still running, same port).
	if rec := do(t, srv, admin, http.MethodPost, "/api/v0/admin/challenges/"+id+"/instance", ""); rec.Code != http.StatusOK {
		t.Fatalf("re-deploy = %d", rec.Code)
	}

	// Participant board shows has_instance + connection info.
	player := teamUp(t, srv, "player", "player@example.com", "Players")
	board := do(t, srv, player, http.MethodGet, "/api/v0/challenges", "")
	var list []struct {
		Slug           string  `json:"slug"`
		HasInstance    bool    `json:"has_instance"`
		ConnectionInfo *string `json:"connection_info"`
	}
	_ = json.Unmarshal(board.Body.Bytes(), &list)
	found := false
	for _, c := range list {
		if c.Slug == slug {
			found = true
			if !c.HasInstance {
				t.Error("participant board: has_instance false")
			}
			if c.ConnectionInfo == nil || *c.ConnectionInfo != "http://ctf.example.com:30000" {
				t.Errorf("participant connection_info = %v", c.ConnectionInfo)
			}
		}
	}
	if !found {
		t.Fatal("challenge missing from board")
	}

	// Logs endpoint returns something.
	logs := do(t, srv, admin, http.MethodGet, "/api/v0/admin/challenges/"+id+"/instance/logs?tail=50", "")
	if logs.Code != http.StatusOK {
		t.Fatalf("logs = %d", logs.Code)
	}

	// Destroy → the port frees and the instance is gone.
	if rec := do(t, srv, admin, http.MethodDelete, "/api/v0/admin/challenges/"+id+"/instance", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("destroy = %d", rec.Code)
	}
	if rec := do(t, srv, admin, http.MethodGet, "/api/v0/admin/challenges/"+id+"/instance", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after destroy = %d, want 404", rec.Code)
	}
	// A fresh deploy reuses the freed lowest port (30000).
	rec = do(t, srv, admin, http.MethodPost, "/api/v0/admin/challenges/"+id+"/instance", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &inst)
	if inst.HostPort == nil || *inst.HostPort != 30000 {
		t.Errorf("redeploy host_port = %v, want 30000 (freed)", inst.HostPort)
	}
}

// TestDeployRejectsStandardChallenge verifies a standard challenge cannot deploy.
func TestDeployRejectsStandardChallenge(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	q := gen.New(pool)
	now := time.Now().UTC()
	_, _ = q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	})
	sessions := auth.NewSessionStore(rdb, time.Hour)
	mgr := runtime.NewManager(runtime.NewFakeRuntime(q), q, "h", 30000, 30999)
	h := handlers.New(handlers.Deps{
		Users: users.New(q, sessions, true), Teams: teams.New(pool, 4),
		Events: events.New(q, clock.System()), Challenges: challenges.New(q, newMemStore()),
		Runtime: mgr, Auth: auth.NewEmailPasswordProvider(q, nil), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, discardLog()), SessionTTL: time.Hour,
	})
	srv := httpserver.New(httpserver.Deps{Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin})
	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	id, _ := createChallenge(t, srv, admin,
		`{"title":"Std","category":"misc","flag":"OSCTF{s}","scoring":"static","points_initial":50,"visible":true}`)
	if rec := do(t, srv, admin, http.MethodPost, "/api/v0/admin/challenges/"+id+"/instance", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("deploy standard = %d, want 403", rec.Code)
	}
}
