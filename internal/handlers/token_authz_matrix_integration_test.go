//go:build integration

package handlers_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/testsupport"
)

// Token-authz matrix (P2-b2): the auth-mode dimension of the policy matrix, kept as its OWN
// test rather than multiplied into the 1400-cell cookie matrix (that would push it past a
// minute and make it the job people skip). It asserts the property the reviewer named: a
// route reachable with a token enforces EXACTLY what it enforces with a cookie — asserted per
// route, not assumed — plus the two token-specific rules: token management is session-only,
// and scope narrows (never widens) what the role allows.
//
// isTokenMgmt reports the session-only token-management routes (a bearer credential is
// rejected on them regardless of scope).
func isTokenMgmt(path string) bool {
	return strings.HasPrefix(path, "/tokens") || strings.HasPrefix(path, "/admin/tokens")
}

func TestTokenAuthzMatrixIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)
	tok := auth.NewTokenService(gen.New(pool))

	admin := makeAdmin(t, srv, pool, "azm", "azm@x.test")
	adminID := userIDByEmail(t, pool, "azm@x.test")
	// A full-scope token for the admin: it should be able to do exactly what the admin's
	// cookie can, everywhere except the session-only token-management routes.
	fullToken, _ := mintToken(t, tok, adminID, []string{auth.ScopeRead, auth.ScopeSubmit, auth.ScopeAdmin}, nil)

	// Precheck: the admin cookie genuinely authenticates, else every comparison below is moot.
	if rec := do(t, srv, admin, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusOK {
		t.Fatalf("precheck: admin cookie /auth/me = %d (%s)", rec.Code, rec.Body)
	}
	// logout would destroy the shared admin session; give that one cell a throwaway jar.
	logoutJar := makeAdmin(t, srv, pool, "azm2", "azm2@x.test")

	routes := enumerateAPIRoutes(t)
	equal, mgmt := 0, 0
	for _, rt := range routes {
		path := substituteDummies(rt.pattern)
		label := rt.method + " " + path
		// createToken needs a valid body to reach the session gate (an empty body is rejected
		// by request validation first, at 400, before requireSession runs).
		body := ""
		if rt.method == http.MethodPost && path == "/tokens" {
			body = `{"name":"m","scopes":["read"]}`
		}
		cookieJar := admin
		if rt.method == http.MethodPost && path == "/auth/logout" {
			cookieJar = logoutJar
		}

		cookieRec := do(t, srv, cookieJar, rt.method, "/api/v1"+path, body)
		tokenRec := doBearer(t, srv, fullToken, rt.method, "/api/v1"+path, body)

		if isTokenMgmt(path) {
			// Session-only: the full-scope bearer token is rejected on token management.
			if tokenRec.Code != http.StatusForbidden {
				t.Errorf("%s: token management via bearer = %d; want 403 (session-only)", label, tokenRec.Code)
			}
			mgmt++
			continue
		}
		// Everywhere else: the token enforces exactly what the cookie enforces.
		if cookieRec.Code != tokenRec.Code {
			t.Errorf("%s: cookie=%d but token=%d — token auth does not enforce the same policy as cookie auth",
				label, cookieRec.Code, tokenRec.Code)
		}
		equal++
	}
	t.Logf("token-authz matrix: %d routes — %d cookie≡token comparisons, %d session-only rejections", len(routes), equal, mgmt)

	// Scope narrows, never widens: a read-only token cannot submit or reach admin, even though
	// the SAME user's cookie can. This is the scope half of role ∩ scope.
	readToken, _ := mintToken(t, tok, adminID, []string{auth.ScopeRead}, nil)
	dummySlug := "no-such-slug-tz"
	if rec := doBearer(t, srv, readToken, http.MethodPost, "/api/v1/challenges/"+dummySlug+"/submit", `{"flag":"x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("read-only token on a submit route = %d; want 403 (scope narrows)", rec.Code)
	}
	if rec := doBearer(t, srv, readToken, http.MethodGet, "/api/v1/admin/stats", ""); rec.Code != http.StatusForbidden {
		t.Errorf("read-only token on an admin route = %d; want 403 (scope narrows)", rec.Code)
	}
	// And the admin's own cookie DOES reach admin (so the narrowing above is meaningful).
	if rec := do(t, srv, admin, http.MethodGet, "/api/v1/admin/stats", ""); rec.Code != http.StatusOK {
		t.Fatalf("admin cookie on /admin/stats = %d; want 200 (else the scope-narrowing check is vacuous)", rec.Code)
	}
}
