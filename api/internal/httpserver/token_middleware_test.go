package httpserver

import (
	"net/http"
	"testing"

	"github.com/osctf/platform/internal/auth"
)

func TestRequiredScope(t *testing.T) {
	cases := []struct {
		method, path, want string
	}{
		{http.MethodGet, "/challenges", auth.ScopeRead},
		{http.MethodGet, "/scoreboard", auth.ScopeRead},
		{http.MethodPost, "/challenges/x/submit", auth.ScopeSubmit},
		{http.MethodPost, "/teams", auth.ScopeSubmit},
		{http.MethodDelete, "/challenges/x/instance", auth.ScopeSubmit},
		{http.MethodGet, "/admin/challenges", auth.ScopeAdmin},
		{http.MethodPost, "/admin/challenges", auth.ScopeAdmin},
		{http.MethodDelete, "/admin/instances/x", auth.ScopeAdmin},
		{http.MethodGet, "/admin", auth.ScopeAdmin}, // exact /admin, no trailing slash
	}
	for _, c := range cases {
		if got := requiredScope(c.method, c.path); got != c.want {
			t.Errorf("requiredScope(%s %s) = %q, want %q", c.method, c.path, got, c.want)
		}
	}
}

func TestScopeAllows(t *testing.T) {
	// An admin-scoped token does NOT implicitly satisfy read/submit — scopes are an explicit
	// set, so a token must carry exactly what the route class needs.
	if scopeAllows([]string{auth.ScopeAdmin}, http.MethodGet, "/challenges") {
		t.Error("admin scope should not satisfy a read route on its own")
	}
	if !scopeAllows([]string{auth.ScopeRead, auth.ScopeSubmit}, http.MethodGet, "/challenges") {
		t.Error("read scope should satisfy a GET")
	}
	if !scopeAllows([]string{auth.ScopeSubmit}, http.MethodPost, "/challenges/x/submit") {
		t.Error("submit scope should satisfy a participant mutation")
	}
	if scopeAllows([]string{auth.ScopeRead, auth.ScopeSubmit}, http.MethodGet, "/admin/challenges") {
		t.Error("read+submit must NOT reach an admin route")
	}
	if !scopeAllows([]string{auth.ScopeAdmin}, http.MethodGet, "/admin/challenges") {
		t.Error("admin scope should satisfy an admin route")
	}
	// An empty scope set permits nothing.
	if scopeAllows(nil, http.MethodGet, "/challenges") {
		t.Error("a scopeless token must be permitted nothing")
	}
}

func TestBearerFromHeader(t *testing.T) {
	mk := func(v string) *http.Request {
		r, _ := http.NewRequest(http.MethodGet, "/", nil)
		if v != "" {
			r.Header.Set("Authorization", v)
		}
		return r
	}
	if _, ok := bearerFromHeader(mk("")); ok {
		t.Error("no Authorization header should yield no bearer")
	}
	if _, ok := bearerFromHeader(mk("Basic abc")); ok {
		t.Error("a non-Bearer scheme should yield no bearer")
	}
	if _, ok := bearerFromHeader(mk("Bearer ")); ok {
		t.Error("an empty bearer value should yield no bearer")
	}
	got, ok := bearerFromHeader(mk("Bearer osctf_pat_abc"))
	if !ok || got != "osctf_pat_abc" {
		t.Errorf("bearerFromHeader = %q,%v; want osctf_pat_abc,true", got, ok)
	}
}
