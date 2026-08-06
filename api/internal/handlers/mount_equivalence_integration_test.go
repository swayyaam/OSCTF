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

// assertMountEquiv2xx sends the same request to both mounts and asserts a genuine 2xx with
// identical bodies (request_id stripped) and the correct deprecation-header split. The
// wantCode sanity-check keeps the comparison from being two identical rejections.
func assertMountEquiv2xx(t *testing.T, srv http.Handler, jar *cookieJar, method, path, body, label string, wantCode int) {
	t.Helper()
	r0 := do(t, srv, jar, method, "/api/v0"+path, body)
	r1 := do(t, srv, jar, method, "/api/v1"+path, body)
	if r0.Code != wantCode {
		t.Fatalf("%s: v0 = %d (%s); want %d — read/mutation did not succeed, equivalence would be vacuous", label, r0.Code, r0.Body, wantCode)
	}
	if r0.Code != r1.Code {
		t.Errorf("%s: status v0=%d v1=%d — mount drift on a 2xx path", label, r0.Code, r1.Code)
	}
	if b0, b1 := canonJSON(r0.Body.Bytes()), canonJSON(r1.Body.Bytes()); b0 != b1 {
		t.Errorf("%s: 2xx body differs across mounts — mount drift:\n  v0=%s\n  v1=%s", label, b0, b1)
	}
	if r0.Header().Get("Deprecation") != "true" || r0.Header().Get("Sunset") == "" {
		t.Errorf("%s: v0 missing Deprecation/Sunset on a 2xx response", label)
	}
	if r1.Header().Get("Deprecation") != "" || r1.Header().Get("Sunset") != "" {
		t.Errorf("%s: v1 carries deprecation headers on a 2xx response", label)
	}
}

// TestMountEquivalence2xxAndMutationIntegration complements the hermetic sweep: that sweep
// only exercises rejection paths (dummy params, empty bodies), so a middleware difference
// affecting SUCCESSFUL requests — body handling, content negotiation, a header set only on
// 2xx — would not show. This proves the 2xx path is equivalent across mounts too: a handful
// of real reads and one real mutation, each compared v0-vs-v1.
func TestMountEquivalence2xxAndMutationIntegration(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb) // full stack + a running event

	admin := makeAdmin(t, srv, pool, "root", "root@x.test")
	const flag = "OSCTF{mount_equiv_2xx}"
	_, slug := createChallenge(t, srv, admin, staticChallengeBody("MountEquiv", flag)) // no max_attempts
	team := teamUp(t, srv, "meq", "meq@x.test", "MeqTeam")
	_, teamID := meOf(t, srv, team)

	// --- real 2xx READS, compared across mounts (before any submit, so attempts_used=0 on
	// both and the challenge detail bodies match). ---
	assertMountEquiv2xx(t, srv, team, http.MethodGet, "/scoreboard", "", "getScoreboard", http.StatusOK)
	assertMountEquiv2xx(t, srv, team, http.MethodGet, "/challenges", "", "listChallenges", http.StatusOK)
	assertMountEquiv2xx(t, srv, team, http.MethodGet, "/challenges/"+slug, "", "getChallenge", http.StatusOK)
	assertMountEquiv2xx(t, srv, team, http.MethodGet, "/teams/"+teamID, "", "getTeam", http.StatusOK)

	// --- real MUTATION, compared across mounts: a wrong-flag submit hits the write path
	// (records a submission, consumes an attempt) on each mount and must return the same
	// verdict shape. The team has unlimited attempts, so the second submit isn't capped. ---
	assertMountEquiv2xx(t, srv, team, http.MethodPost, "/challenges/"+slug+"/submit",
		`{"flag":"OSCTF{wrong}"}`, "submitFlag.wrong", http.StatusOK)

	// Prove the mutation genuinely ran the submit path (not some early identical rejection):
	// the verdict is a real incorrect result.
	rec := do(t, srv, team, http.MethodPost, "/api/v1/challenges/"+slug+"/submit", `{"flag":"OSCTF{wrong}"}`)
	var v struct {
		Correct bool `json:"correct"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.Correct {
		t.Errorf("submitFlag.wrong verdict = %s (err %v); want {correct:false} — the mutation path was not exercised", rec.Body, err)
	}
}
