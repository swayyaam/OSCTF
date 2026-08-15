//go:build integration

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/osctf/platform/internal/testsupport"
)

// Token management endpoints (P2-b2): create/list/revoke ownership + expiry + plaintext-once,
// all session-authenticated (the session-only gate itself is asserted in the token-authz
// matrix). The DTO structurally omits the hash; these tests pin the plaintext-once and
// cross-user-isolation behavior, and that ban deletes the rows (defense-in-depth).

type createdToken struct {
	Id        string    `json:"id"`
	Token     string    `json:"token"`
	Prefix    string    `json:"prefix"`
	ExpiresAt time.Time `json:"expires_at"`
}

func createTokenAPI(t *testing.T, srv http.Handler, jar *cookieJar, body string) createdToken {
	t.Helper()
	rec := do(t, srv, jar, http.MethodPost, "/api/v1/tokens", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("createToken = %d (%s)", rec.Code, rec.Body)
	}
	var c createdToken
	if err := json.Unmarshal(rec.Body.Bytes(), &c); err != nil {
		t.Fatalf("decode created token: %v", err)
	}
	if c.Token == "" || !strings.HasPrefix(c.Token, "osctf_pat_") {
		t.Fatalf("create response missing the plaintext token: %s", rec.Body)
	}
	return c
}

func TestTokenEndpointsIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)
	ctx := context.Background()

	t.Run("plaintext once; list is metadata-only", func(t *testing.T) {
		u := registerUser(t, srv, "tep_a", "tep_a@x.test")
		c := createTokenAPI(t, srv, u, `{"name":"laptop","scopes":["read"]}`)

		rec := do(t, srv, u, http.MethodGet, "/api/v1/tokens", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("listTokens = %d", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, c.Token) {
			t.Error("listTokens leaked the plaintext token")
		}
		if strings.Contains(body, "hash") || strings.Contains(body, "token_hash") {
			t.Error("listTokens exposed a hash field")
		}
		// The metadata IS present (id + prefix), so the list is useful without the secret.
		if !strings.Contains(body, c.Prefix) || !strings.Contains(body, c.Id) {
			t.Errorf("listTokens missing prefix/id metadata: %s", body)
		}
	})

	t.Run("expiry: default applied, over-max rejected, valid honored", func(t *testing.T) {
		u := registerUser(t, srv, "tep_x", "tep_x@x.test")
		// Omitted → default (90 days).
		c := createTokenAPI(t, srv, u, `{"name":"d","scopes":["read"]}`)
		days := time.Until(c.ExpiresAt).Hours() / 24
		if days < 89 || days > 91 {
			t.Errorf("default expiry ≈ %.1f days, want ~90", days)
		}
		// Over the max (365) → 422.
		if rec := do(t, srv, u, http.MethodPost, "/api/v1/tokens", `{"name":"big","scopes":["read"],"expires_in_days":400}`); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("expires_in_days=400 = %d; want 422", rec.Code)
		}
		// A valid explicit lifetime is honored.
		c2 := createTokenAPI(t, srv, u, `{"name":"m","scopes":["read"],"expires_in_days":30}`)
		if d := time.Until(c2.ExpiresAt).Hours() / 24; d < 29 || d > 31 {
			t.Errorf("expires_in_days=30 → %.1f days, want ~30", d)
		}
	})

	t.Run("ownership: a user cannot see or revoke another's token", func(t *testing.T) {
		a := registerUser(t, srv, "tep_o1", "tep_o1@x.test")
		b := registerUser(t, srv, "tep_o2", "tep_o2@x.test")
		ta := createTokenAPI(t, srv, a, `{"name":"a","scopes":["read"]}`)

		// B's list does not include A's token.
		if body := do(t, srv, b, http.MethodGet, "/api/v1/tokens", "").Body.String(); strings.Contains(body, ta.Id) {
			t.Error("user B's listTokens shows user A's token")
		}
		// B revoking A's token → 404 (indistinguishable from nonexistent).
		if rec := do(t, srv, b, http.MethodDelete, "/api/v1/tokens/"+ta.Id, ""); rec.Code != http.StatusNotFound {
			t.Errorf("B revoking A's token = %d; want 404", rec.Code)
		}
		// A's token still works (B's attempt didn't revoke it).
		if rec := doBearer(t, srv, ta.Token, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusOK {
			t.Fatalf("A's token after B's failed revoke = %d; want 200", rec.Code)
		}
		// A revokes it → 204, and it dies immediately.
		if rec := do(t, srv, a, http.MethodDelete, "/api/v1/tokens/"+ta.Id, ""); rec.Code != http.StatusNoContent {
			t.Fatalf("A revoking own token = %d; want 204", rec.Code)
		}
		if rec := doBearer(t, srv, ta.Token, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("revoked token still works = %d; want 401", rec.Code)
		}
	})

	t.Run("admin lists and revokes anyone's token", func(t *testing.T) {
		admin := makeAdmin(t, srv, pool, "tep_adm", "tep_adm@x.test")
		victim := registerUser(t, srv, "tep_v", "tep_v@x.test")
		tv := createTokenAPI(t, srv, victim, `{"name":"v","scopes":["read"]}`)

		rec := do(t, srv, admin, http.MethodGet, "/api/v1/admin/tokens", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("adminListTokens = %d", rec.Code)
		}
		if b := rec.Body.String(); !strings.Contains(b, tv.Id) || !strings.Contains(b, "tep_v") {
			t.Errorf("adminListTokens missing the victim token/owner: %s", b)
		}
		// Admin revokes the victim's token → it dies.
		if rec := do(t, srv, admin, http.MethodDelete, "/api/v1/admin/tokens/"+tv.Id, ""); rec.Code != http.StatusNoContent {
			t.Fatalf("adminRevokeToken = %d; want 204", rec.Code)
		}
		if rec := doBearer(t, srv, tv.Token, http.MethodGet, "/api/v1/auth/me", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("admin-revoked token still works = %d; want 401", rec.Code)
		}
	})

	t.Run("ban deletes the user's token rows (defense-in-depth)", func(t *testing.T) {
		admin := makeAdmin(t, srv, pool, "tep_ba", "tep_ba@x.test")
		victim := registerUser(t, srv, "tep_bv", "tep_bv@x.test")
		createTokenAPI(t, srv, victim, `{"name":"b","scopes":["read"]}`)
		vid := userIDByEmail(t, pool, "tep_bv@x.test")

		if rec := do(t, srv, admin, http.MethodPatch, "/api/v1/admin/users/"+vid.String(), `{"banned":true}`); rec.Code != http.StatusOK {
			t.Fatalf("ban = %d (%s)", rec.Code, rec.Body)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM api_tokens WHERE user_id=$1`, vid).Scan(&n); err != nil {
			t.Fatalf("count tokens: %v", err)
		}
		if n != 0 {
			t.Errorf("banned user still has %d token rows; want 0 (deleted as hygiene)", n)
		}
	})
}
