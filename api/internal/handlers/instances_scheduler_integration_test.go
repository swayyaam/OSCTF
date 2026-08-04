//go:build integration

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/flags"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/runtime"
	"github.com/osctf/platform/internal/scheduler"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
)

// schedulerServer wires a full server with the scheduler and a fake runtime.
func schedulerServer(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client, quota int) http.Handler {
	t.Helper()
	q := gen.New(pool)
	now := time.Now().UTC()
	if _, err := q.CreateEvent(context.Background(), gen.CreateEventParams{
		ID: uuid.Must(uuid.NewV7()), Name: "CTF", Description: "d",
		StartsAt: now.Add(-time.Hour), EndsAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("event: %v", err)
	}
	sessions := auth.NewSessionStore(rdb, time.Hour)
	clk := clock.System()
	ev := events.New(q, clk)
	mgr := runtime.NewManager(runtime.NewFakeRuntime(q), q, "ctf.example.com", 30000, 30999)
	sched := scheduler.New(mgr, q, ev, flags.NewGenerator("osctf"), audit.New(q, discardLog()), clk, discardLog(),
		scheduler.Config{TTL: time.Hour, Extend: 30 * time.Minute, MaxTTL: 4 * time.Hour, Quota: quota})
	h := handlers.New(handlers.Deps{
		Users: users.New(q, sessions, true), Teams: teams.New(pool, 4),
		Events: ev, Challenges: challenges.New(q, newMemStore()),
		Runtime: mgr, Scheduler: sched,
		Auth: auth.NewEmailPasswordProvider(q, nil), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, discardLog()), SessionTTL: time.Hour,
	})
	return httpserver.New(httpserver.Deps{Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin})
}

const perTeamChallenge = `{
	"title":"PT","category":"web","kind":"container","flag":"OSCTF{placeholder}",
	"image":"osctf/example:0.2","internal_port":8000,"connection_template":"http://{host}:{port}",
	"scoring":"static","points_initial":100,"visible":true,
	"instancing":"per_team","flag_mode":"per_instance"
}`

func TestPerTeamInstanceEndpointsIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := schedulerServer(t, pool, rdb, 3)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	_, slug := createChallenge(t, srv, admin, perTeamChallenge)
	player := teamUp(t, srv, "player", "player@example.com", "Players")
	base := "/api/v0/challenges/" + slug + "/instance"

	// Start -> 201 with a running instance and no flag field.
	rec := do(t, srv, player, http.MethodPost, base, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("start = %d (%s)", rec.Code, rec.Body)
	}
	if strings.Contains(strings.ToLower(rec.Body.String()), "flag") {
		t.Errorf("SECURITY: instance payload mentions a flag: %s", rec.Body)
	}
	var inst struct {
		State     string  `json:"state"`
		HostPort  *int    `json:"host_port"`
		ExpiresAt *string `json:"expires_at"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &inst)
	if inst.State != "running" || inst.HostPort == nil {
		t.Fatalf("start payload = %+v", inst)
	}
	if inst.ExpiresAt == nil {
		t.Error("start payload missing expires_at")
	}

	// Start again -> 200 idempotent.
	if rec := do(t, srv, player, http.MethodPost, base, ""); rec.Code != http.StatusOK {
		t.Fatalf("idempotent start = %d (%s)", rec.Code, rec.Body)
	}

	// Extend -> 200, expiry pushed forward.
	rec = do(t, srv, player, http.MethodPost, base+"/extend", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("extend = %d (%s)", rec.Code, rec.Body)
	}
	var ext struct {
		ExpiresAt *string `json:"expires_at"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ext)
	if ext.ExpiresAt == nil || *ext.ExpiresAt <= *inst.ExpiresAt {
		t.Errorf("extend did not push expiry: %v -> %v", inst.ExpiresAt, ext.ExpiresAt)
	}

	// The challenge detail carries the team's instance.
	detail := do(t, srv, player, http.MethodGet, "/api/v0/challenges/"+slug, "")
	if !strings.Contains(detail.Body.String(), `"instancing":"per_team"`) {
		t.Errorf("detail missing instancing: %s", detail.Body)
	}

	// Admin fleet view lists the per-team instance with an owner, no flag.
	fleet := do(t, srv, admin, http.MethodGet, "/api/v0/admin/instances", "")
	if fleet.Code != http.StatusOK {
		t.Fatalf("admin list = %d", fleet.Code)
	}
	if strings.Contains(strings.ToLower(fleet.Body.String()), "osctf{") {
		t.Errorf("SECURITY: admin fleet leaked a flag: %s", fleet.Body)
	}
	if !strings.Contains(fleet.Body.String(), `"team_name":"Players"`) {
		t.Errorf("admin fleet missing owner name: %s", fleet.Body)
	}
	// With no unadopted resources, the optional list fields must be OMITTED
	// (not [] and not null) through the handler — the 4a-i structural guard.
	if strings.Contains(fleet.Body.String(), `"unadopted"`) || strings.Contains(fleet.Body.String(), `"unadopted_networks"`) {
		t.Errorf("unadopted/unadopted_networks must be omitted at zero, got: %s", fleet.Body)
	}

	// Stop -> 204, then detail no longer has a running instance.
	if rec := do(t, srv, player, http.MethodDelete, base, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("stop = %d (%s)", rec.Code, rec.Body)
	}
}

func TestPerTeamQuotaAndGuardsIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := schedulerServer(t, pool, rdb, 1) // quota 1

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	_, slugA := createChallenge(t, srv, admin, perTeamChallenge)
	_, slugB := createChallenge(t, srv, admin, strings.Replace(perTeamChallenge, `"title":"PT"`, `"title":"PT2"`, 1))
	_, sharedSlug := createChallenge(t, srv, admin, `{
		"title":"Shared","category":"web","kind":"container","flag":"OSCTF{s}",
		"image":"osctf/example:0.1","internal_port":8000,"scoring":"static","points_initial":100,"visible":true
	}`)
	player := teamUp(t, srv, "quotan", "quotan@example.com", "Quota")

	// First start ok.
	if rec := do(t, srv, player, http.MethodPost, "/api/v0/challenges/"+slugA+"/instance", ""); rec.Code != http.StatusCreated {
		t.Fatalf("start A = %d (%s)", rec.Code, rec.Body)
	}
	// Second start (different challenge) exceeds quota=1 -> 409.
	if rec := do(t, srv, player, http.MethodPost, "/api/v0/challenges/"+slugB+"/instance", ""); rec.Code != http.StatusConflict {
		t.Fatalf("start B at quota = %d, want 409", rec.Code)
	}
	// Start on a shared challenge -> 409 (not per-team).
	if rec := do(t, srv, player, http.MethodPost, "/api/v0/challenges/"+sharedSlug+"/instance", ""); rec.Code != http.StatusConflict {
		t.Fatalf("start shared = %d, want 409", rec.Code)
	}
	// Stopping A frees the slot; B can start.
	if rec := do(t, srv, player, http.MethodDelete, "/api/v0/challenges/"+slugA+"/instance", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("stop A = %d", rec.Code)
	}
	if rec := do(t, srv, player, http.MethodPost, "/api/v0/challenges/"+slugB+"/instance", ""); rec.Code != http.StatusCreated {
		t.Fatalf("start B after freeing = %d (%s)", rec.Code, rec.Body)
	}
}

func TestAuthoringRejectsPerTeamOnStandardIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := schedulerServer(t, pool, rdb, 3)
	admin := makeAdmin(t, srv, pool, "root", "root@example.com")

	rec := do(t, srv, admin, http.MethodPost, "/api/v0/admin/challenges", `{
		"title":"Bad","category":"misc","flag":"OSCTF{x}","scoring":"static","points_initial":50,
		"visible":true,"instancing":"per_team"
	}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("per_team on standard = %d, want 422 (%s)", rec.Code, rec.Body)
	}
}
