//go:build integration

package handlers_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/osctf/platform/internal/testsupport"
)

// TestChallengeTypeRejectUnregistered pins the write-time rejection of an unregistered
// challenge type on both create and update, in the two shapes the reviewer called out:
//   - a typo of a real built-in ("standrd"), and
//   - a valid-looking id for a plugin this deployment does not have ("regex-flag").
//
// Both must be rejected with 422 and an error that NAMES the offending type, so an admin
// who fat-fingers a type or authors against a missing plugin fails on write rather than
// storing a type nothing can resolve.
func TestChallengeTypeRejectUnregistered(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb) // wires a real Challenges service (create hits the DB)
	admin := makeAdmin(t, srv, pool, "root", "root@example.com")

	bad := []struct {
		name  string
		ctype string
	}{
		{"typo-of-builtin", "standrd"},             // fat-fingered "standard"
		{"valid-shape-unregistered", "regex-flag"}, // plausible plugin type, not registered here
	}

	// --- CREATE: an unregistered type is rejected, naming it. ---
	for _, tc := range bad {
		t.Run("create/"+tc.name, func(t *testing.T) {
			body := staticChallengeBody("Reject "+tc.name, "OSCTF{reject}")
			// splice a "type" into the otherwise-valid static body.
			body = strings.TrimSuffix(body, "}") + `,"type":"` + tc.ctype + `"}`
			rec := do(t, srv, admin, http.MethodPost, "/api/v0/admin/challenges", body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("create with type %q = %d (%s); want 422", tc.ctype, rec.Code, rec.Body)
			}
			assertTypeErrorNames(t, rec.Body.Bytes(), tc.ctype)
		})
	}

	// --- UPDATE: create a valid challenge, then PATCH it to an unregistered type. ---
	validID, _ := createChallenge(t, srv, admin, staticChallengeBody("Valid", "OSCTF{ok}"))
	for _, tc := range bad {
		t.Run("update/"+tc.name, func(t *testing.T) {
			rec := do(t, srv, admin, http.MethodPatch, "/api/v0/admin/challenges/"+validID,
				`{"type":"`+tc.ctype+`"}`)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("update to type %q = %d (%s); want 422", tc.ctype, rec.Code, rec.Body)
			}
			assertTypeErrorNames(t, rec.Body.Bytes(), tc.ctype)
		})
	}

	// A registered type (the built-in "standard") still succeeds on update — the guard
	// rejects only the unknown, it doesn't reject the field itself.
	if rec := do(t, srv, admin, http.MethodPatch, "/api/v0/admin/challenges/"+validID,
		`{"type":"standard"}`); rec.Code != http.StatusOK {
		t.Fatalf("update to a registered type = %d (%s); want 200", rec.Code, rec.Body)
	}
}

// assertTypeErrorNames checks the 422 body is a Problem whose `type` field errors name the
// offending value — the reviewer's requirement that the error identify the bad type.
func assertTypeErrorNames(t *testing.T, body []byte, ctype string) {
	t.Helper()
	var p struct {
		Errors map[string][]string `json:"errors"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v (body %s)", err, body)
	}
	msgs, ok := p.Errors["type"]
	if !ok {
		t.Fatalf("no 'type' field error in %s", body)
	}
	joined := strings.Join(msgs, " ")
	if !strings.Contains(joined, ctype) {
		t.Errorf("type error does not name the offending value %q: %q", ctype, joined)
	}
}
