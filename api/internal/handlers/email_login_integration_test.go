//go:build integration

package handlers_test

import (
	"net/http"
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

// serverEmailDisabled is newTestServer with the built-in email/password login turned off
// (an SSO-only deployment), everything else identical.
func serverEmailDisabled(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client) http.Handler {
	t.Helper()
	q := gen.New(pool)
	sessions := auth.NewSessionStore(rdb, time.Hour)
	h := handlers.New(handlers.Deps{
		Users:              users.New(q, sessions, true),
		Teams:              teams.New(pool, 4),
		Events:             events.New(q, clock.System()),
		Auth:               auth.NewRegistry(auth.NewEmailPasswordProvider(q, nil)),
		EmailLoginDisabled: true,
		Sessions:           sessions,
		Limiter:            redisx.NewLimiter(rdb),
		Audit:              audit.New(q, discardLog()),
		SessionTTL:         time.Hour,
	})
	return httpserver.New(httpserver.Deps{Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin})
}

// TestEmailLoginDisabledIntegration: with OSCTF_AUTH_EMAIL_LOGIN=false, POST /auth/login is
// refused with a clear error, but the rest of the auth surface behaves sensibly for an
// already-authenticated session — session validation, change-password, and logout all work.
func TestEmailLoginDisabledIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := serverEmailDisabled(t, pool, rdb)
	jar := &cookieJar{}

	// Register is not gated by the login toggle, so it still works and yields a session —
	// standing in for an existing/authenticated user (one who logged in before the toggle
	// was flipped, or via an SSO provider).
	if rec := do(t, srv, jar, http.MethodPost, "/api/v0/auth/register",
		`{"username":"sso","email":"sso@example.com","password":"supersecret1"}`); rec.Code != http.StatusCreated {
		t.Fatalf("register = %d, want 201 (%s)", rec.Code, rec.Body)
	}

	// Login is refused with a clear 403 (not a generic 401) — a fresh jar, since login
	// must not depend on an existing session.
	if rec := do(t, srv, &cookieJar{}, http.MethodPost, "/api/v0/auth/login",
		`{"email":"sso@example.com","password":"supersecret1"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("login with email disabled = %d, want 403", rec.Code)
	}

	// The existing session still validates.
	if rec := do(t, srv, jar, http.MethodGet, "/api/v0/auth/me", ""); rec.Code != http.StatusOK {
		t.Fatalf("me (existing session) = %d, want 200", rec.Code)
	}

	// Change-password works for the existing session (keeps this session alive).
	if rec := do(t, srv, jar, http.MethodPatch, "/api/v0/auth/me/password",
		`{"current_password":"supersecret1","new_password":"evensecreter2"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("change-password (existing session) = %d, want 204 (%s)", rec.Code, rec.Body)
	}

	// Logout works, and afterwards the session is gone.
	if rec := do(t, srv, jar, http.MethodPost, "/api/v0/auth/logout", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", rec.Code)
	}
	if rec := do(t, srv, jar, http.MethodGet, "/api/v0/auth/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d, want 401", rec.Code)
	}
}
