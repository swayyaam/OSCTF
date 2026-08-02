//go:build integration

package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/osctf/platform/internal/testsupport"
)

// 3b — enumeration safety. A hidden (or, before the event starts, any) challenge must be
// INDISTINGUISHABLE from a nonexistent one to a participant: same status, same body (bar
// the per-request id), and comparable timing — otherwise ID probing reveals which slugs
// exist. Every participant-facing endpoint that takes a slug is probed across a visible,
// a hidden, and a nonexistent id, and hidden is asserted identical to nonexistent.

const (
	visContainer = `{"title":%q,"category":"web","kind":"container","flag":"OSCTF{x}","image":"osctf/example:0.1","internal_port":8000,"connection_template":"http://{host}:{port}","scoring":"static","points_initial":100,"visible":true,"instancing":"per_team"}`
	hidContainer = `{"title":%q,"category":"web","kind":"container","flag":"OSCTF{x}","image":"osctf/example:0.1","internal_port":8000,"connection_template":"http://{host}:{port}","scoring":"static","points_initial":100,"visible":false,"instancing":"per_team"}`
)

// problemSansID returns the problem+json body with the per-request request_id removed and
// keys canonicalized, so two 404s are comparable regardless of their (differing) nonce.
func problemSansID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		return "<non-json:" + rec.Body.String() + ">"
	}
	delete(m, "request_id")
	b, _ := json.Marshal(m) // map keys marshal sorted → canonical
	return string(b)
}

func TestEnumerationHiddenChallengeIndistinguishable(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb) // full stack (submissions + runtime + scheduler), running event

	admin := makeAdmin(t, srv, pool, "root", "root@x.test")
	_, visSlug := createChallenge(t, srv, admin, fmt.Sprintf(visContainer, "Visible"))
	_, hidSlug := createChallenge(t, srv, admin, fmt.Sprintf(hidContainer, "Hidden"))
	const noneSlug = "no-such-challenge-4f2a"

	player := teamUp(t, srv, "probe", "probe@x.test", "Probers")
	attID := "00000000-0000-0000-0000-000000000000"

	P := "/api/v0/challenges/"
	probes := []struct {
		name         string
		method       string
		path         func(slug string) string
		body         string
		visibleIs2xx bool // whether the visible id yields a 2xx (so the probe can prove it distinguishes)
	}{
		{"getChallenge", http.MethodGet, func(s string) string { return P + s }, "", true},
		{"downloadAttachment", http.MethodGet, func(s string) string { return P + s + "/attachments/" + attID }, "", false},
		{"submitFlag", http.MethodPost, func(s string) string { return P + s + "/submit" }, `{"flag":"OSCTF{guess}"}`, true},
		{"startInstance", http.MethodPost, func(s string) string { return P + s + "/instance" }, "", true},
		{"stopInstance", http.MethodDelete, func(s string) string { return P + s + "/instance" }, "", false},
		{"extendInstance", http.MethodPost, func(s string) string { return P + s + "/instance/extend" }, "", false},
	}

	for _, p := range probes {
		hid := do(t, srv, player, p.method, p.path(hidSlug), p.body)
		none := do(t, srv, player, p.method, p.path(noneSlug), p.body)

		if hid.Code != none.Code {
			t.Errorf("%s: hidden status %d != nonexistent %d — existence leak", p.name, hid.Code, none.Code)
		}
		if hid.Code != http.StatusNotFound {
			t.Errorf("%s: hidden = %d, want 404 (a hidden challenge must read as absent)", p.name, hid.Code)
		}
		if hb, nb := problemSansID(t, hid), problemSansID(t, none); hb != nb {
			t.Errorf("%s: hidden body != nonexistent body — existence leak:\n  hidden=%s\n  none  =%s", p.name, hb, nb)
		}

		// Sanity: where a visible id yields a 2xx, the probe genuinely distinguishes
		// existence — so the identical hidden/nonexistent results above are meaningful.
		if p.visibleIs2xx {
			if vis := do(t, srv, player, p.method, p.path(visSlug), p.body); vis.Code == http.StatusNotFound {
				t.Errorf("%s: visible id also 404 — the probe cannot distinguish existence, test is vacuous", p.name)
			}
		}
	}

	// Timing: a hidden lookup HITS a row; a nonexistent one MISSES — the classic shape for
	// a timing oracle. Sample 200 of each, interleaved (so GC/scheduling noise spreads
	// across both), and report median AND p95. The bound is lenient (one query's hit-vs-miss
	// is microseconds); a gross asymmetry — building a full detail before rejecting — trips it.
	const iters = 200
	hidDs := make([]time.Duration, 0, iters)
	noneDs := make([]time.Duration, 0, iters)
	for i := 0; i < iters; i++ {
		t0 := time.Now()
		do(t, srv, player, http.MethodGet, P+hidSlug, "")
		hidDs = append(hidDs, time.Since(t0))
		t1 := time.Now()
		do(t, srv, player, http.MethodGet, P+noneSlug, "")
		noneDs = append(noneDs, time.Since(t1))
	}
	hidMed, hidP95 := medianP95(hidDs)
	noneMed, noneP95 := medianP95(noneDs)
	t.Logf("getChallenge timing (n=%d): hidden median=%s p95=%s | nonexistent median=%s p95=%s",
		iters, hidMed, hidP95, noneMed, noneP95)
	slow, fast := hidMed, noneMed
	if fast > slow {
		slow, fast = noneMed, hidMed
	}
	if slow > 3*fast+100*time.Microsecond {
		t.Errorf("getChallenge median timing asymmetry: hidden=%s nonexistent=%s (>3x) — a timing side channel reveals existence", hidMed, noneMed)
	}

	// Unreleased (before the event starts): the phase gate answers before any lookup, so
	// EVERY challenge — visible included — reads identically to a nonexistent one.
	future := time.Now().UTC().Add(time.Hour)
	if _, err := pool.Exec(context.Background(), `UPDATE events SET starts_at=$1, ends_at=$2`, future, future.Add(time.Hour)); err != nil {
		t.Fatalf("set pre-event: %v", err)
	}
	pv := do(t, srv, player, http.MethodGet, P+visSlug, "")
	ph := do(t, srv, player, http.MethodGet, P+hidSlug, "")
	pn := do(t, srv, player, http.MethodGet, P+noneSlug, "")
	if pv.Code == http.StatusOK || pv.Code == http.StatusNotFound {
		t.Errorf("pre-event visible getChallenge = %d, want a phase rejection that hides existence", pv.Code)
	}
	for _, c := range []struct {
		name string
		rec  *httptest.ResponseRecorder
	}{{"visible", pv}, {"hidden", ph}} {
		if c.rec.Code != pn.Code {
			t.Errorf("pre-event %s status %d != nonexistent %d — an unreleased challenge is distinguishable", c.name, c.rec.Code, pn.Code)
		}
		if b, nb := problemSansID(t, c.rec), problemSansID(t, pn); b != nb {
			t.Errorf("pre-event %s body != nonexistent body — an unreleased challenge is distinguishable", c.name)
		}
	}
}

// TestEnumerationOtherTeamInstanceIndistinguishable (3b): a participant on team A must not
// be able to tell that team B has an instance on a challenge. Participants have no
// instance-by-ID route (the only one, adminDestroyInstanceById, is admin-only), so the
// cross-team surface is the per-challenge instance routes — which key on the CALLER's team.
// A's view of a challenge where B has an instance must match A's view of one where nobody
// does: same 404 from stop/extend, and no sign of B's instance in the challenge detail.
func TestEnumerationOtherTeamInstanceIndistinguishable(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)

	admin := makeAdmin(t, srv, pool, "root", "root@x.test")
	_, shared := createChallenge(t, srv, admin, fmt.Sprintf(visContainer, "Shared"))
	_, fresh := createChallenge(t, srv, admin, fmt.Sprintf(visContainer, "Fresh"))

	teamB := teamUp(t, srv, "bteam", "b@x.test", "BTeam")
	if rec := do(t, srv, teamB, http.MethodPost, "/api/v0/challenges/"+shared+"/instance", ""); rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("teamB start on shared = %d (%s)", rec.Code, rec.Body)
	}
	teamA := teamUp(t, srv, "ateam", "a@x.test", "ATeam")

	// Sanity: B, the owner, DOES see its instance — so "A sees nothing" is meaningful.
	if !hasInstance(t, srv, teamB, shared) {
		t.Fatal("owner teamB does not see its own instance — test would be vacuous")
	}

	// Stop / extend on the shared challenge (where B has an instance) must be identical to
	// the fresh challenge (where nobody does): A has no instance on either → 404, same body.
	for _, r := range []struct {
		name, method, suffix string
	}{
		{"stopInstance", http.MethodDelete, "/instance"},
		{"extendInstance", http.MethodPost, "/instance/extend"},
	} {
		withB := do(t, srv, teamA, r.method, "/api/v0/challenges/"+shared+r.suffix, "")
		without := do(t, srv, teamA, r.method, "/api/v0/challenges/"+fresh+r.suffix, "")
		if withB.Code != without.Code {
			t.Errorf("%s: status differs by another team's instance: shared=%d fresh=%d", r.name, withB.Code, without.Code)
		}
		if bb, fb := problemSansID(t, withB), problemSansID(t, without); bb != fb {
			t.Errorf("%s: body differs by another team's instance — leak:\n  shared=%s\n  fresh =%s", r.name, bb, fb)
		}
	}

	// A's challenge detail must NOT reveal B's instance.
	if hasInstance(t, srv, teamA, shared) {
		t.Error("teamA's view of the shared challenge shows has_instance=true — another team's instance leaked")
	}
}

// TestEnumerationAdminRoutesNoExistenceLeak (3b-i): admin routes are the back door to the
// participant guarantee. requireAdmin must fire BEFORE any slug/id resolution, so a
// non-admin gets the same rejection for an existing target as for a nonexistent one — else
// probing admin paths (e.g. /admin/challenges/{id}) reveals which ids exist.
func TestEnumerationAdminRoutesNoExistenceLeak(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)

	admin := makeAdmin(t, srv, pool, "root", "root@x.test")
	realID, realSlug := createChallenge(t, srv, admin, fmt.Sprintf(visContainer, "Real"))
	const fakeID = "11111111-1111-1111-1111-111111111111" // valid uuid, no such row
	_ = realSlug

	participant := teamUp(t, srv, "nonadm", "nonadm@x.test", "NonAdmins")
	anon := &cookieJar{}

	adminProbes := []struct {
		name, method string
		real, fake   string
	}{
		{"adminGetChallenge", http.MethodGet, "/api/v0/admin/challenges/" + realID, "/api/v0/admin/challenges/" + fakeID},
		{"adminGetInstance", http.MethodGet, "/api/v0/admin/challenges/" + realID + "/instance", "/api/v0/admin/challenges/" + fakeID + "/instance"},
		{"adminDestroyInstanceById", http.MethodDelete, "/api/v0/admin/instances/" + fakeID, "/api/v0/admin/instances/" + fakeID},
	}
	for _, caller := range []struct {
		who string
		jar *cookieJar
	}{{"participant", participant}, {"anon", anon}} {
		for _, p := range adminProbes {
			re := do(t, srv, caller.jar, p.method, p.real, "")
			fa := do(t, srv, caller.jar, p.method, p.fake, "")
			if re.Code != fa.Code {
				t.Errorf("%s as %s: existing target %d != nonexistent %d — requireAdmin does not gate before resolution", p.name, caller.who, re.Code, fa.Code)
			}
			if re.Code == http.StatusOK {
				t.Errorf("%s as %s: non-admin got 200 — admin route not gated", p.name, caller.who)
			}
			if rb, fb := problemSansID(t, re), problemSansID(t, fa); rb != fb {
				t.Errorf("%s as %s: body differs by target existence — existence leak", p.name, caller.who)
			}
		}
	}
}

// hasInstance reports the caller team's has_instance for a challenge, and fails if the
// detail exposes an instance object to a team that owns none (a stronger leak check).
func hasInstance(t *testing.T, srv http.Handler, jar *cookieJar, slug string) bool {
	t.Helper()
	rec := do(t, srv, jar, http.MethodGet, "/api/v0/challenges/"+slug, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("getChallenge %s = %d (%s)", slug, rec.Code, rec.Body)
	}
	var d struct {
		HasInstance bool             `json:"has_instance"`
		Instance    *json.RawMessage `json:"instance"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	return d.HasInstance || d.Instance != nil
}

func medianP95(ds []time.Duration) (median, p95 time.Duration) {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2], s[int(float64(len(s))*0.95)]
}
