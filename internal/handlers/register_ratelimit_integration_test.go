//go:build integration

package handlers_test

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/clock"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/events"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
)

// registerServer builds a minimal server whose registration rate limit is configured
// exactly as given (burst/window), so a test can drive the per-IP limit directly.
func registerServer(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client, burst int, window time.Duration) http.Handler {
	t.Helper()
	q := gen.New(pool)
	sessions := auth.NewSessionStore(rdb, time.Hour)
	h := handlers.New(handlers.Deps{
		Users: users.New(q, sessions, true), Teams: teams.New(pool, 4),
		Events: events.New(q, clock.System()),
		Auth:   auth.NewRegistry(auth.NewEmailPasswordProvider(q, nil)), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, discardLog()),
		SessionTTL:      time.Hour,
		RegisterIPBurst: burst, RegisterIPWindow: window,
	})
	return httpserver.New(httpserver.Deps{Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin})
}

// registerFromIP posts a registration whose client address is exactly ip, so every call
// shares one IP (bypassing the do() helper's per-request IP randomisation).
func registerFromIP(t *testing.T, srv http.Handler, ip, username string) int {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"email":%q,"password":"supersecret1"}`, username, username+"@venue.test")
	r := httptest.NewRequest(http.MethodPost, "/api/v0/auth/register", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", testOrigin)
	r.RemoteAddr = ip + ":44444"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec.Code
}

// TestRegisterRateLimitAllowsVenueBurst (GitHub issue #1): a venue is a hundred-plus
// players registering from one NAT in the first couple of minutes. Under the shipped
// default, all of them must succeed; the old hard-coded 5/hour/IP let five through.
func TestRegisterRateLimitAllowsVenueBurst(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)

	// Configured with the shipped defaults: OSCTF_REGISTER_IP_BURST=500 / _WINDOW=600s.
	srv := registerServer(t, pool, rdb, 500, 10*time.Minute)
	const oneNAT = "203.0.113.55"
	const players = 100
	for i := 0; i < players; i++ {
		if code := registerFromIP(t, srv, oneNAT, fmt.Sprintf("venue%03d", i)); code != http.StatusCreated {
			t.Fatalf("registration %d/%d from one IP = %d — the default per-IP limit rejected a venue burst", i+1, players, code)
		}
	}

	// Not vacuous: the limit IS wired and controllable. With a burst of 3, the 4th
	// registration from one IP is rejected, and a different IP is unaffected.
	tight := registerServer(t, pool, rdb, 3, 10*time.Minute)
	for i := 0; i < 3; i++ {
		if code := registerFromIP(t, tight, "198.51.100.7", fmt.Sprintf("tight%d", i)); code != http.StatusCreated {
			t.Fatalf("registration %d under burst=3 = %d, want 201", i+1, code)
		}
	}
	if code := registerFromIP(t, tight, "198.51.100.7", "tight-over"); code != http.StatusTooManyRequests {
		t.Fatalf("4th registration under burst=3 = %d, want 429 (limit not enforced)", code)
	}
	if code := registerFromIP(t, tight, "198.51.100.99", "other-ip"); code != http.StatusCreated {
		t.Fatalf("registration from a different IP = %d, want 201 (limit is per-IP)", code)
	}
}
