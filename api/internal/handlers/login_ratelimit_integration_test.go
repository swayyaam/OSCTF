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

// loginServer builds a server whose per-IP login limit is configured exactly as
// given. Registration is left generous so seeding the cohort is never the thing
// under test.
func loginServer(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client, burst int, window time.Duration) http.Handler {
	t.Helper()
	q := gen.New(pool)
	sessions := auth.NewSessionStore(rdb, time.Hour)
	h := handlers.New(handlers.Deps{
		Users: users.New(q, sessions, true), Teams: teams.New(pool, 4),
		Events: events.New(q, clock.System()),
		Auth:   auth.NewRegistry(auth.NewEmailPasswordProvider(q, nil)), Sessions: sessions,
		Limiter: redisx.NewLimiter(rdb), Audit: audit.New(q, discardLog()),
		SessionTTL:      time.Hour,
		RegisterIPBurst: 10000, RegisterIPWindow: time.Minute,
		LoginIPBurst: burst, LoginIPWindow: window,
	})
	return httpserver.New(httpserver.Deps{Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin})
}

// loginFromIP posts a login whose client address is exactly ip, so a whole cohort
// can share one IP (bypassing the do() helper's per-request IP randomisation).
func loginFromIP(t *testing.T, srv http.Handler, ip, email, password string) int {
	t.Helper()
	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	r := httptest.NewRequest(http.MethodPost, "/api/v0/auth/login", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", testOrigin)
	r.RemoteAddr = ip + ":55555"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec.Code
}

// TestLoginRateLimitAllowsVenueCohort (GitHub issue #4): a shared-NAT venue logging
// in at event start must not be throttled. A 30-person cohort behind one IP all log
// in under the shipped default; the old hard-coded 10/5min/IP let ten through.
func TestLoginRateLimitAllowsVenueCohort(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := loginServer(t, pool, rdb, 500, 10*time.Minute) // shipped default

	const cohort = 30
	const nat = "203.0.113.7"
	for i := 0; i < cohort; i++ {
		u := fmt.Sprintf("venue%02d", i)
		if code := registerFromIP(t, srv, fmt.Sprintf("198.51.100.%d", i+1), u); code != http.StatusCreated {
			t.Fatalf("setup register %s: %d", u, code)
		}
	}
	for i := 0; i < cohort; i++ {
		email := fmt.Sprintf("venue%02d@venue.test", i)
		if code := loginFromIP(t, srv, nat, email, "supersecret1"); code != http.StatusOK {
			t.Fatalf("venue member %d logging in from the shared NAT = %d, want 200 (login-ip limit too tight for a venue)", i, code)
		}
	}
}

// TestLoginRateLimitTightConfigStillFires: a tight burst still throttles a single
// IP, and a different IP has its own bucket.
func TestLoginRateLimitTightConfigStillFires(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := loginServer(t, pool, rdb, 3, time.Minute)

	if code := registerFromIP(t, srv, "198.51.100.200", "tight"); code != http.StatusCreated {
		t.Fatalf("setup register: %d", code)
	}
	email, pw := "tight@venue.test", "supersecret1"
	for i := 0; i < 3; i++ {
		if code := loginFromIP(t, srv, "203.0.113.9", email, pw); code != http.StatusOK {
			t.Fatalf("login %d from one IP = %d, want 200", i, code)
		}
	}
	if code := loginFromIP(t, srv, "203.0.113.9", email, pw); code != http.StatusTooManyRequests {
		t.Fatalf("4th login from the same IP = %d, want 429", code)
	}
	if code := loginFromIP(t, srv, "203.0.113.10", email, pw); code != http.StatusOK {
		t.Fatalf("login from a fresh IP = %d, want 200 (per-IP buckets are independent)", code)
	}
}
