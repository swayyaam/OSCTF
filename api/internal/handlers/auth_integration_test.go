package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/osctf/platform/internal/audit"
	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/handlers"
	"github.com/osctf/platform/internal/httpserver"
	"github.com/osctf/platform/internal/redisx"
	"github.com/osctf/platform/internal/teams"
	"github.com/osctf/platform/internal/testsupport"
	"github.com/osctf/platform/internal/users"
)

const testOrigin = "http://localhost:8080"

func newTestServer(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client) http.Handler {
	t.Helper()
	q := gen.New(pool)
	sessions := auth.NewSessionStore(rdb, time.Hour)
	usersSvc := users.New(q, sessions, true)
	teamsSvc := teams.New(pool, 4)
	provider := auth.NewEmailPasswordProvider(q, nil)
	h := handlers.New(handlers.Deps{
		Users:      usersSvc,
		Teams:      teamsSvc,
		Auth:       provider,
		Sessions:   sessions,
		Limiter:    redisx.NewLimiter(rdb),
		Audit:      audit.New(q, discardLog()),
		SessionTTL: time.Hour,
	})
	return httpserver.New(httpserver.Deps{
		Log:        discardLog(),
		Handlers:   h,
		Sessions:   sessions,
		BaseOrigin: testOrigin,
	})
}

func TestAuthFlowIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := newTestServer(t, pool, rdb)

	jar := &cookieJar{}

	// Register → 201, cookie set.
	rec := do(t, srv, jar, http.MethodPost, "/api/v0/auth/register",
		`{"username":"alice","email":"alice@example.com","password":"supersecret1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register = %d, want 201 (%s)", rec.Code, rec.Body)
	}

	// Me → 200, username matches.
	rec = do(t, srv, jar, http.MethodGet, "/api/v0/auth/me", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("me = %d, want 200", rec.Code)
	}
	var me struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me.Username != "alice" || me.Role != "user" {
		t.Fatalf("me = %+v", me)
	}

	// Logout → 204, then me → 401.
	if rec := do(t, srv, jar, http.MethodPost, "/api/v0/auth/logout", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", rec.Code)
	}
	if rec := do(t, srv, jar, http.MethodGet, "/api/v0/auth/me", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me after logout = %d, want 401", rec.Code)
	}

	// Wrong password → generic 401.
	rec = do(t, srv, jar, http.MethodPost, "/api/v0/auth/login",
		`{"email":"alice@example.com","password":"nope"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("login wrong = %d, want 401", rec.Code)
	}

	// Correct login → 200.
	rec = do(t, srv, jar, http.MethodPost, "/api/v0/auth/login",
		`{"email":"alice@example.com","password":"supersecret1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200", rec.Code)
	}

	// Missing Origin on a mutating request → 403 (CSRF).
	req := httptest.NewRequest(http.MethodPost, "/api/v0/auth/logout", nil)
	jar.apply(req)
	noOrigin := httptest.NewRecorder()
	srv.ServeHTTP(noOrigin, req)
	if noOrigin.Code != http.StatusForbidden {
		t.Fatalf("logout without Origin = %d, want 403", noOrigin.Code)
	}
}

// --- tiny cookie jar / request helper ---------------------------------------

type cookieJar struct{ cookies []*http.Cookie }

func (j *cookieJar) apply(r *http.Request) {
	for _, c := range j.cookies {
		r.AddCookie(c)
	}
}

func (j *cookieJar) update(resp *http.Response) {
	j.cookies = append(j.cookies, resp.Cookies()...)
}

// reqCounter gives each request a distinct client IP so IP-based rate limits
// (register-ip, login-ip) don't cross-contaminate functional tests. Rate limiting
// itself is verified separately (see the M2 curl checks / TestRateLimit).
var reqCounter atomic.Uint32

func do(t *testing.T, srv http.Handler, jar *cookieJar, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Origin", testOrigin)
	n := reqCounter.Add(1)
	r.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:40000", (n>>16)&0xff, (n>>8)&0xff, n&0xff)
	jar.apply(r)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	jar.update(rec.Result())
	return rec
}
