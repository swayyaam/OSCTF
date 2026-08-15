//go:build integration

package handlers_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/db/gen"
	"github.com/osctf/platform/internal/testsupport"
)

// Token auth (P2-b1). These tests prove the security properties of bearer API tokens: the
// token is read only from the Authorization header, bearer skips CSRF, scope ∩ role is
// enforced, the owner's role/ban is resolved live (so demotion/ban/revocation/expiry take
// effect on the NEXT request), unknown scopes fail closed, and the prefix lookup is not a
// timing/enumeration oracle. Token containment (a live token never leaks) is asserted in the
// flag-containment scanner.

// doBearer makes a request authenticated by a Bearer token and DELIBERATELY sets no Origin
// header — a bearer request must skip CSRF, so omitting Origin proves the skip (a session
// mutation without Origin is 403).
func doBearer(t *testing.T, srv http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	n := reqCounter.Add(1)
	r.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:41000", (n>>16)&0xff, (n>>8)&0xff, n&0xff)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, r)
	return rec
}

func userIDByEmail(t *testing.T, pool *pgxpool.Pool, email string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `SELECT id FROM users WHERE email=$1`, email).Scan(&id); err != nil {
		t.Fatalf("user id for %s: %v", email, err)
	}
	return id
}

func mintToken(t *testing.T, tok *auth.TokenService, userID uuid.UUID, scopes []string, expiresAt *time.Time) (plaintext string, id uuid.UUID) {
	t.Helper()
	pt, meta, err := tok.Create(context.Background(), userID, "test", scopes, expiresAt)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return pt, meta.ID
}

func TestTokenAuthIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)
	q := gen.New(pool)
	tok := auth.NewTokenService(q)
	ctx := context.Background()

	// A shared admin + a static challenge for submit tests.
	admin := makeAdmin(t, srv, pool, "troot", "troot@x.test")
	_, statSlug := createChallenge(t, srv, admin, staticChallengeBody("TokChal", "OSCTF{tok}"))

	t.Run("bearer authenticates like a session", func(t *testing.T) {
		u := teamUp(t, srv, "tok_a", "tok_a@x.test", "TokA")
		uid, _ := meOf(t, srv, u)
		token, _ := mintToken(t, tok, uuid.MustParse(uid), []string{auth.ScopeRead}, nil)
		if rec := doBearer(t, srv, token, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusOK {
			t.Fatalf("bearer GET /auth/me = %d (%s); want 200", rec.Code, rec.Body)
		}
	})

	t.Run("token accepted only from the Authorization header", func(t *testing.T) {
		u := teamUp(t, srv, "tok_h", "tok_h@x.test", "TokH")
		uid, _ := meOf(t, srv, u)
		token, _ := mintToken(t, tok, uuid.MustParse(uid), []string{auth.ScopeRead}, nil)

		// Control: the Authorization header authenticates.
		if rec := doBearer(t, srv, token, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusOK {
			t.Fatalf("control (header) = %d; want 200", rec.Code)
		}

		// The same token value via a cookie (session name AND a custom name), a query param,
		// or a form field must NOT authenticate — else CSRF protection silently vanishes.
		mkCookie := func(name string) *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			r.AddCookie(&http.Cookie{Name: name, Value: token})
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, r)
			return rec
		}
		if rec := mkCookie(auth.CookieName); rec.Code != http.StatusUnauthorized {
			t.Errorf("token in %q cookie = %d; want 401 (must not authenticate)", auth.CookieName, rec.Code)
		}
		if rec := mkCookie("token"); rec.Code != http.StatusUnauthorized {
			t.Errorf("token in custom cookie = %d; want 401", rec.Code)
		}
		for _, qp := range []string{"token", "access_token"} {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me?"+qp+"="+token, nil)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, r)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("token in ?%s= = %d; want 401", qp, rec.Code)
			}
		}
		// Form field on a POST (Origin set so we isolate auth from CSRF): must not authenticate.
		r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", bytes.NewBufferString("token="+token))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", testOrigin)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("token in a form field = %d; want 401", rec.Code)
		}
	})

	t.Run("bearer mutation skips CSRF; session mutation without Origin is 403", func(t *testing.T) {
		u := teamUp(t, srv, "tok_c", "tok_c@x.test", "TokC")
		uid, _ := meOf(t, srv, u)
		token, _ := mintToken(t, tok, uuid.MustParse(uid), []string{auth.ScopeRead, auth.ScopeSubmit}, nil)

		// Bearer submit with NO Origin header → succeeds (CSRF skipped for bearer).
		rec := doBearer(t, srv, token, http.MethodPost, "/api/v1/challenges/"+statSlug+"/submit", `{"flag":"OSCTF{wrong}"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("bearer submit (no Origin) = %d (%s); want 200", rec.Code, rec.Body)
		}

		// The SAME mutation via the session cookie without an Origin header → 403 (CSRF).
		r := httptest.NewRequest(http.MethodPost, "/api/v1/challenges/"+statSlug+"/submit", bytes.NewBufferString(`{"flag":"OSCTF{wrong}"}`))
		r.Header.Set("Content-Type", "application/json")
		u.apply(r) // session cookie, but no Origin
		sRec := httptest.NewRecorder()
		srv.ServeHTTP(sRec, r)
		if sRec.Code != http.StatusForbidden {
			t.Errorf("session submit without Origin = %d; want 403 (CSRF still applies to cookies)", sRec.Code)
		}
	})

	t.Run("role intersect scope", func(t *testing.T) {
		// A non-admin user whose token carries the admin scope: the admin route is still 403,
		// because scope never grants what the role lacks (the role gate re-reads the user).
		u := teamUp(t, srv, "tok_rs", "tok_rs@x.test", "TokRS")
		uid, _ := meOf(t, srv, u)
		adminScoped, _ := mintToken(t, tok, uuid.MustParse(uid), []string{auth.ScopeRead, auth.ScopeAdmin}, nil)
		if rec := doBearer(t, srv, adminScoped, http.MethodGet, "/api/v1/admin/stats", ""); rec.Code != http.StatusForbidden {
			t.Errorf("user-role token with admin scope on /admin/stats = %d; want 403 (role lacks admin)", rec.Code)
		}

		// A read-only token cannot submit (scope gate); a submit token can.
		readOnly, _ := mintToken(t, tok, uuid.MustParse(uid), []string{auth.ScopeRead}, nil)
		if rec := doBearer(t, srv, readOnly, http.MethodPost, "/api/v1/challenges/"+statSlug+"/submit", `{"flag":"OSCTF{x}"}`); rec.Code != http.StatusForbidden {
			t.Errorf("read-only token submitting = %d; want 403 (scope gate)", rec.Code)
		}
		// And a read token cannot reach an admin route regardless of role.
		if rec := doBearer(t, srv, readOnly, http.MethodGet, "/api/v1/admin/stats", ""); rec.Code != http.StatusForbidden {
			t.Errorf("read token on /admin/stats = %d; want 403 (scope gate)", rec.Code)
		}
	})

	t.Run("demotion takes effect on the next request", func(t *testing.T) {
		makeAdmin(t, srv, pool, "tok_dm", "tok_dm@x.test")
		uid := userIDByEmail(t, pool, "tok_dm@x.test")
		token, _ := mintToken(t, tok, uid, []string{auth.ScopeRead, auth.ScopeAdmin}, nil)
		if rec := doBearer(t, srv, token, http.MethodGet, "/api/v1/admin/stats", ""); rec.Code != http.StatusOK {
			t.Fatalf("admin token on /admin/stats = %d; want 200", rec.Code)
		}
		role := "user"
		if _, err := q.UpdateUserAdminFields(ctx, gen.UpdateUserAdminFieldsParams{ID: uid, Role: &role}); err != nil {
			t.Fatalf("demote: %v", err)
		}
		if rec := doBearer(t, srv, token, http.MethodGet, "/api/v1/admin/stats", ""); rec.Code != http.StatusForbidden {
			t.Errorf("after demotion the SAME token on /admin/stats = %d; want 403 (live role)", rec.Code)
		}
	})

	t.Run("ban takes effect on the next request", func(t *testing.T) {
		u := teamUp(t, srv, "tok_bn", "tok_bn@x.test", "TokBN")
		uid, _ := meOf(t, srv, u)
		token, _ := mintToken(t, tok, uuid.MustParse(uid), []string{auth.ScopeRead}, nil)
		if rec := doBearer(t, srv, token, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusOK {
			t.Fatalf("token before ban = %d; want 200", rec.Code)
		}
		banned := true
		if _, err := q.UpdateUserAdminFields(ctx, gen.UpdateUserAdminFieldsParams{ID: uuid.MustParse(uid), Banned: &banned}); err != nil {
			t.Fatalf("ban: %v", err)
		}
		if rec := doBearer(t, srv, token, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("after ban the SAME token = %d; want 401 (live ban check)", rec.Code)
		}
	})

	t.Run("revocation takes effect on the next request", func(t *testing.T) {
		u := teamUp(t, srv, "tok_rv", "tok_rv@x.test", "TokRV")
		uid, _ := meOf(t, srv, u)
		token, tid := mintToken(t, tok, uuid.MustParse(uid), []string{auth.ScopeRead}, nil)
		if rec := doBearer(t, srv, token, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusOK {
			t.Fatalf("token before revoke = %d; want 200", rec.Code)
		}
		if ok, err := tok.Revoke(ctx, uuid.MustParse(uid), tid); err != nil || !ok {
			t.Fatalf("revoke: ok=%v err=%v", ok, err)
		}
		if rec := doBearer(t, srv, token, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("after revoke the SAME token = %d; want 401 (immediate, no cache)", rec.Code)
		}
	})

	t.Run("expired token rejected", func(t *testing.T) {
		u := teamUp(t, srv, "tok_ex", "tok_ex@x.test", "TokEX")
		uid, _ := meOf(t, srv, u)
		past := time.Now().Add(-time.Hour)
		token, _ := mintToken(t, tok, uuid.MustParse(uid), []string{auth.ScopeRead}, &past)
		if rec := doBearer(t, srv, token, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("expired token = %d; want 401", rec.Code)
		}
	})

	t.Run("unknown scope fails closed", func(t *testing.T) {
		u := teamUp(t, srv, "tok_us", "tok_us@x.test", "TokUS")
		uid, _ := meOf(t, srv, u)
		// Insert a token with an unknown scope DIRECTLY (bypassing Create's validation) to
		// simulate version skew / a manual DB edit; auth must reject it rather than continue.
		raw, hash, prefix, _ := auth.GenerateToken()
		if _, err := q.CreateAPIToken(ctx, gen.CreateAPITokenParams{
			ID: uuid.Must(uuid.NewV7()), UserID: uuid.MustParse(uid), Name: "bogus",
			TokenHash: hash, Prefix: prefix, Scopes: []string{"superuser"},
		}); err != nil {
			t.Fatalf("insert bogus-scope token: %v", err)
		}
		if rec := doBearer(t, srv, raw, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusForbidden {
			t.Errorf("token with an unknown scope = %d; want 403 (fail closed)", rec.Code)
		}
	})

	t.Run("no prefix timing or enumeration oracle", func(t *testing.T) {
		u := teamUp(t, srv, "tok_tm", "tok_tm@x.test", "TokTM")
		uid, _ := meOf(t, srv, u)
		real, _ := mintToken(t, tok, uuid.MustParse(uid), []string{auth.ScopeRead}, nil)

		// Same prefix, wrong secret (flip the last char) vs a wholly nonexistent token.
		last := real[len(real)-1]
		repl := byte('A')
		if last == 'A' {
			repl = 'B'
		}
		wrongSecret := real[:len(real)-1] + string(repl)
		nonexistent, _, _, _ := auth.GenerateToken()

		w := doBearer(t, srv, wrongSecret, http.MethodGet, "/api/v1/auth/me", "")
		n := doBearer(t, srv, nonexistent, http.MethodGet, "/api/v1/auth/me", "")
		if w.Code != http.StatusUnauthorized || n.Code != http.StatusUnauthorized {
			t.Fatalf("both must be 401: wrong=%d nonexistent=%d", w.Code, n.Code)
		}
		if wb, nb := problemSansID(t, w), problemSansID(t, n); wb != nb {
			t.Errorf("valid-prefix-wrong-secret body != nonexistent body — an enumeration oracle:\n  wrong=%s\n  none =%s", wb, nb)
		}

		// Timing: a valid prefix (a candidate row exists) must not be distinguishable from a
		// nonexistent one. Interleave samples; the bound is lenient (same shape as the
		// enumeration hidden-vs-nonexistent test).
		const iters = 150
		wDs := make([]time.Duration, 0, iters)
		nDs := make([]time.Duration, 0, iters)
		for i := 0; i < iters; i++ {
			t0 := time.Now()
			doBearer(t, srv, wrongSecret, http.MethodGet, "/api/v1/auth/me", "")
			wDs = append(wDs, time.Since(t0))
			t1 := time.Now()
			doBearer(t, srv, nonexistent, http.MethodGet, "/api/v1/auth/me", "")
			nDs = append(nDs, time.Since(t1))
		}
		wMed, _ := medianP95(wDs)
		nMed, _ := medianP95(nDs)
		slow, fast := wMed, nMed
		if fast > slow {
			slow, fast = nMed, wMed
		}
		t.Logf("token lookup timing (n=%d): valid-prefix-wrong-secret median=%s | nonexistent median=%s", iters, wMed, nMed)
		if slow > 3*fast+100*time.Microsecond {
			t.Errorf("prefix timing asymmetry: wrong-secret=%s nonexistent=%s (>3x) — a timing oracle on token existence", wMed, nMed)
		}
	})
}
