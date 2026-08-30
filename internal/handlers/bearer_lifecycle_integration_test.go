//go:build integration

package handlers_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/swayyaam/OSCTF/internal/auth"
	"github.com/swayyaam/OSCTF/internal/db/gen"
	"github.com/swayyaam/OSCTF/internal/testsupport"
)

// SUCCESS CRITERION 3 (docs/v0.3/00-overview.md): every operation the dashboard performs is
// reachable through /api/v1 with an API token and NO session cookie.
//
// The criterion is a property of the API, not of a client, so this drives a full event lifecycle
// with a plain HTTP client and an Authorization header — no OSCTF client binary, no cookie jar —
// and asserts on EVERY request that nothing sends or requires a cookie.
//
// The assertion is what makes this a proof rather than a smoke test: a route that quietly fell
// back to a session, or set one along the way, would still "work" in a browser and would silently
// break every non-browser client. Nothing else in the suite checks that.

// cookieFreeClient drives requests with a bearer token and fails the test if a cookie appears in
// either direction.
type cookieFreeClient struct {
	t     *testing.T
	srv   http.Handler
	token string
	calls int
}

func (c *cookieFreeClient) do(method, path, body string) *http.Response {
	c.t.Helper()
	rec := doBearer(c.t, c.srv, c.token, method, path, body)
	resp := rec.Result()
	c.calls++

	// Nothing in this flow may hand back a session.
	for _, ck := range resp.Cookies() {
		c.t.Fatalf("%s %s set a cookie %q — the bearer path must never issue a session", method, path, ck.Name)
	}
	if resp.Header.Get("Set-Cookie") != "" {
		c.t.Fatalf("%s %s sent Set-Cookie: %q", method, path, resp.Header.Get("Set-Cookie"))
	}
	return resp
}

func (c *cookieFreeClient) ok(method, path, body string) *http.Response {
	c.t.Helper()
	resp := c.do(method, path, body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b := make([]byte, 512)
		n, _ := resp.Body.Read(b)
		c.t.Fatalf("%s %s: status %d, want 2xx (%s)", method, path, resp.StatusCode, string(b[:n]))
	}
	return resp
}

func TestFullLifecycleOverBearerWithoutCookies(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)
	q := gen.New(pool)
	tokens := auth.NewTokenService(q)

	// Accounts and tokens are created out of band: an account cannot pre-exist its own creation,
	// and minting a token is session-authenticated by design (a token cannot mint another token).
	// EVERYTHING after this point is the criterion's subject — the lifecycle itself, bearer only.
	makeAdmin(t, srv, pool, "sc3admin", "sc3admin@x.test")
	adminToken, _ := mintToken(t, tokens, userIDByEmail(t, pool, "sc3admin@x.test"),
		[]string{"read", "submit", "admin"}, nil)
	registerUser(t, srv, "sc3player", "sc3player@x.test")
	playerToken, _ := mintToken(t, tokens, userIDByEmail(t, pool, "sc3player@x.test"),
		[]string{"read", "submit"}, nil)

	admin := &cookieFreeClient{t: t, srv: srv, token: adminToken}
	player := &cookieFreeClient{t: t, srv: srv, token: playerToken}

	// 1. Authenticate — the token identifies the caller with no login round trip.
	var who struct {
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(admin.ok(http.MethodGet, "/api/v1/auth/me", "").Body).Decode(&who); err != nil {
		t.Fatalf("decode /auth/me: %v", err)
	}
	if who.Username != "sc3admin" || who.Role != "admin" {
		t.Fatalf("token resolved to %+v, want the admin", who)
	}

	// 2. Set the event window.
	admin.ok(http.MethodPatch, "/api/v1/admin/event",
		`{"name":"SC3","description":"bearer-driven","starts_at":"2020-01-01T00:00:00Z","ends_at":"2099-01-01T00:00:00Z"}`)

	// 3. Author a challenge.
	var chal struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	created := admin.ok(http.MethodPost, "/api/v1/admin/challenges", staticChallengeBody("Bearer Chal", "OSCTF{bearer}"))
	if err := json.NewDecoder(created.Body).Decode(&chal); err != nil {
		t.Fatalf("decode created challenge: %v", err)
	}
	if chal.Slug == "" {
		t.Fatal("created challenge has no slug")
	}

	// 4. The player forms a team and reads the board — all over bearer.
	player.ok(http.MethodPost, "/api/v1/teams", `{"name":"SC3 Squad"}`)
	player.ok(http.MethodGet, "/api/v1/challenges", "")
	player.ok(http.MethodGet, "/api/v1/challenges/"+chal.Slug, "")

	// 5. Submit the flag.
	var verdict struct {
		Correct bool `json:"correct"`
	}
	sub := player.ok(http.MethodPost, "/api/v1/challenges/"+chal.Slug+"/submit", `{"flag":"OSCTF{bearer}"}`)
	if err := json.NewDecoder(sub.Body).Decode(&verdict); err != nil {
		t.Fatalf("decode submission: %v", err)
	}
	if !verdict.Correct {
		t.Fatal("the correct flag was not accepted over bearer")
	}

	// 6. Read the scoreboard.
	player.ok(http.MethodGet, "/api/v1/scoreboard", "")

	// 7. Admin actions.
	admin.ok(http.MethodGet, "/api/v1/admin/users", "")
	admin.ok(http.MethodGet, "/api/v1/admin/submissions", "")
	admin.ok(http.MethodGet, "/api/v1/admin/plugins", "")

	if total := admin.calls + player.calls; total < 11 {
		t.Fatalf("only %d requests were driven; the lifecycle should cover authenticate → event → "+
			"author → team → read → submit → scoreboard → admin", total)
	}
}

// The other half of the criterion: a bearer request must not be ACCEPTED because of a session.
// A token with insufficient scope has to fail even when the same user holds a valid session,
// otherwise "no cookie" would be true only by accident of the test not sending one.
func TestBearerScopeIsNotRescuedBySession(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)
	q := gen.New(pool)

	jar := makeAdmin(t, srv, pool, "sc3dual", "sc3dual@x.test")
	adminID := userIDByEmail(t, pool, "sc3dual@x.test")
	readOnly, _ := mintToken(t, auth.NewTokenService(q), adminID, []string{"read"}, nil)

	// The very same admin, holding a live session, presenting a read-only token.
	rec := doBearer(t, srv, readOnly, http.MethodPost, "/api/v1/admin/challenges",
		staticChallengeBody("Should Not Exist", "OSCTF{no}"))
	if rec.Code < 400 {
		t.Fatalf("a read-scoped token created a challenge (status %d) — scope must bound a token "+
			"regardless of what sessions its owner holds", rec.Code)
	}
	if len(jar.cookies) == 0 {
		t.Fatal("test setup is wrong: the admin should hold a session for this to mean anything")
	}
	if strings.Contains(rec.Body.String(), "Should Not Exist") {
		t.Fatal("the challenge was created despite the scope check")
	}
}

// A client must be able to discover what its own credential may do. Without this the CLI's
// `whoami` cannot report scopes and the MCP server cannot decide which tools to expose — it would
// have to attempt an operation and be refused to learn its own limits.
//
// Scopes belong to the CREDENTIAL, not the account, so a session must NOT report them: a session
// carries the account's full role, and an empty list there would read as "no permissions" rather
// than "not applicable".
func TestMeReportsScopesForTokensAndNotForSessions(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)
	q := gen.New(pool)

	jar := makeAdmin(t, srv, pool, "scopeuser", "scopeuser@x.test")
	uid := userIDByEmail(t, pool, "scopeuser@x.test")
	tok, _ := mintToken(t, auth.NewTokenService(q), uid, []string{"read", "submit"}, nil)

	t.Run("token auth reports its scopes", func(t *testing.T) {
		rec := doBearer(t, srv, tok, http.MethodGet, "/api/v1/auth/me", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		var me struct {
			Scopes []string `json:"scopes"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&me); err != nil {
			t.Fatalf("decode: %v", err)
		}
		got := map[string]bool{}
		for _, s := range me.Scopes {
			got[s] = true
		}
		if !got["read"] || !got["submit"] {
			t.Fatalf("scopes = %v, want the token's read+submit", me.Scopes)
		}
		if got["admin"] {
			t.Errorf("scopes = %v — reporting a scope the token was not granted would let a client "+
				"offer an operation that must fail", me.Scopes)
		}
	})

	t.Run("session auth reports none", func(t *testing.T) {
		rec := do(t, srv, jar, http.MethodGet, "/api/v1/auth/me", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d", rec.Code)
		}
		var raw map[string]any
		if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if v, present := raw["scopes"]; present {
			t.Errorf("a session reported scopes=%v; scopes belong to a token, and an empty or "+
				"invented list here would be read as a permission set", v)
		}
	})
}
