//go:build integration

package handlers_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/osctf/platform/internal/apigen"
	"github.com/osctf/platform/internal/testsupport"
)

// Mount equivalence (P2-a). /api/v1 is canonical and /api/v0 is a deprecated alias serving
// the SAME handler value — structurally un-forkable by construction (one `api` handler,
// mounted twice). This test is the behavioural proof of that claim, because "same handler"
// is exactly the thing a future refactor could quietly break:
//
//   - EVERY route is covered, derived from the generated router (chi.Walk over a fresh
//     apigen mux) plus the WebSocket — not a hand-maintained sample that could drift.
//   - For each route, v0 and v1 are compared for IDENTICAL status AND identical body
//     (request_id stripped the way the enumeration test strips it), across several
//     authorization-relevant identities — a middleware difference between the mounts would
//     surface as an authz difference on an authed call, not as a status change on an anon one.
//   - v0 carries `Deprecation: true` + a `Sunset` date; v1 carries neither.
//
// Every probe is hermetic: path params are substituted with nonexistent ids/slugs and bodies
// are empty, so each call resolves to an auth rejection, a validation rejection, a not-found,
// or an idempotent read — never a mutation — and is therefore safe to run on both mounts.

type eqRoute struct{ method, pattern string }

var eqParamRe = regexp.MustCompile(`\{[^}]+\}`)

// substituteDummies replaces each {param} with a nonexistent-but-well-formed value: a slug
// param gets a dummy slug, everything else a valid-shaped UUID that matches no row. Both
// drive the handler to a deterministic 404/400 rather than mutating anything.
func substituteDummies(pattern string) string {
	return eqParamRe.ReplaceAllStringFunc(pattern, func(m string) string {
		name := strings.SplitN(strings.Trim(m, "{}"), ":", 2)[0]
		if strings.Contains(strings.ToLower(name), "slug") {
			return "no-such-slug-eqv"
		}
		return "11111111-1111-1111-1111-111111111111"
	})
}

// enumerateAPIRoutes walks a fresh apigen router for every (method, pattern) it registers,
// then appends the WebSocket route (not an OpenAPI operation). A nil ServerInterface is fine:
// registration/walk never invokes the handlers.
func enumerateAPIRoutes(t *testing.T) []eqRoute {
	t.Helper()
	r := chi.NewRouter()
	_ = apigen.HandlerFromMux(nil, r)
	var routes []eqRoute
	if err := chi.Walk(r, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, eqRoute{method, pattern})
		return nil
	}); err != nil {
		t.Fatalf("walk apigen routes: %v", err)
	}
	routes = append(routes, eqRoute{http.MethodGet, "/ws"})
	if len(routes) < 40 {
		t.Fatalf("enumerated only %d routes — the walker missed the generated surface", len(routes))
	}
	return routes
}

// canonJSON strips request_id at any depth and re-marshals canonically (map keys sort), so
// two responses are comparable regardless of their per-request nonce. Non-JSON bodies
// (204s, the WS error) compare as raw bytes.
func canonJSON(raw []byte) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "raw:" + string(raw)
	}
	stripReqID(v)
	b, _ := json.Marshal(v)
	return string(b)
}

func stripReqID(v any) {
	switch n := v.(type) {
	case map[string]any:
		delete(n, "request_id")
		for _, c := range n {
			stripReqID(c)
		}
	case []any:
		for _, c := range n {
			stripReqID(c)
		}
	}
}

func TestMountEquivalenceIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)

	// Teamless authed identities: every team-mutating route rejects them (not on a team /
	// not captain), so the only route that mutates its own state is logout — special-cased
	// below. anon exercises the no-session path; member the session path; admin the role gate.
	anon := &cookieJar{}
	member := registerUser(t, srv, "equiv_member", "equiv_member@x.test")
	admin := makeAdmin(t, srv, pool, "equiv_admin", "equiv_admin@x.test")
	identities := []struct {
		name string
		jar  *cookieJar
	}{{"anon", anon}, {"member", member}, {"admin", admin}}

	routes := enumerateAPIRoutes(t)
	var uniq int
	comparisons := 0

	for _, rt := range routes {
		path := substituteDummies(rt.pattern)
		isLogout := rt.method == http.MethodPost && path == "/auth/logout"
		for _, id := range identities {
			jar0, jar1 := id.jar, id.jar
			// logout invalidates the caller's own session; give each mount an independent
			// throwaway so the shared identity jars survive the sweep.
			if isLogout && id.name != "anon" {
				uniq++
				jar0 = registerUser(t, srv, fmt.Sprintf("lo0%d", uniq), fmt.Sprintf("lo0%d@x.test", uniq))
				jar1 = registerUser(t, srv, fmt.Sprintf("lo1%d", uniq), fmt.Sprintf("lo1%d@x.test", uniq))
			}

			r0 := do(t, srv, jar0, rt.method, "/api/v0"+path, "")
			r1 := do(t, srv, jar1, rt.method, "/api/v1"+path, "")
			label := fmt.Sprintf("%s %s [%s]", rt.method, path, id.name)

			// Deprecation signal: present on v0, absent on v1.
			if r0.Header().Get("Deprecation") != "true" || r0.Header().Get("Sunset") == "" {
				t.Errorf("%s: v0 missing Deprecation/Sunset (Deprecation=%q Sunset=%q)",
					label, r0.Header().Get("Deprecation"), r0.Header().Get("Sunset"))
			}
			if r1.Header().Get("Deprecation") != "" || r1.Header().Get("Sunset") != "" {
				t.Errorf("%s: v1 carries deprecation headers (Deprecation=%q Sunset=%q) — the alias signal leaked onto the canonical surface",
					label, r1.Header().Get("Deprecation"), r1.Header().Get("Sunset"))
			}

			// Same handler ⇒ identical status and identical body.
			if r0.Code != r1.Code {
				t.Errorf("%s: status v0=%d v1=%d — mount drift", label, r0.Code, r1.Code)
				continue
			}
			if b0, b1 := canonJSON(r0.Body.Bytes()), canonJSON(r1.Body.Bytes()); b0 != b1 {
				t.Errorf("%s: body differs across mounts — mount drift:\n  v0=%s\n  v1=%s", label, b0, b1)
			}
			comparisons++
		}
	}
	t.Logf("mount equivalence: %d routes × %d identities = %d cross-mount comparisons", len(routes), len(identities), comparisons)
}
