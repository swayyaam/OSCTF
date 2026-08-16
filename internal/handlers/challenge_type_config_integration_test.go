//go:build integration

package handlers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/osctf/platform/internal/challenges"
	"github.com/osctf/platform/internal/testsupport"
)

// validatingType is a registered challenge type that exercises author-time type_config validation
// end to end through the admin API — the dial the ValidateConfig reconcile wired. It requires a
// "pattern" key (empty → a per-field rejection) and normalizes it (trims), and when `down` is set it
// fails to reach a verdict at all (a plugin whose process is unavailable), so the write must fail
// CLOSED. It implements only ChallengeType (author-time), not FlagChecker — this test is about the
// write path, not submission.
type validatingType struct{ down *atomic.Bool }

func (validatingType) ID() string { return "validating-probe-type" }

func (v validatingType) ValidateConfig(_ context.Context, cfg map[string]string) (challenges.ConfigValidation, error) {
	if v.down.Load() {
		return challenges.ConfigValidation{}, errors.New("checker unavailable")
	}
	if strings.TrimSpace(cfg["pattern"]) == "" {
		return challenges.ConfigValidation{OK: false, FieldErrors: map[string]string{"pattern": "is required"}}, nil
	}
	// Normalize: the STORED value is the plugin's canonical form (trimmed), not the raw author input.
	return challenges.ConfigValidation{OK: true, Normalized: map[string]string{"pattern": strings.TrimSpace(cfg["pattern"])}}, nil
}

// TestTypeConfigAuthorTimeValidation drives the per-challenge type_config channel end to end:
// author-time ValidateConfig is actually DIALED on create/update (reachability — a "never called"
// regression would make these pass without it), a bad config is a 422 with a per-field error, the
// stored value is the plugin's normalized form, and — when the checker is down — a config change
// fails closed (503) while an unrelated edit still succeeds (fail closed only on a config change).
func TestTypeConfigAuthorTimeValidation(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := matrixServer(t, pool, rdb)
	admin := makeAdmin(t, srv, pool, "root", "root@example.com")

	down := &atomic.Bool{}
	if err := challenges.RegisterType("validating-probe-type", validatingType{down: down}, false); err != nil {
		t.Fatalf("register type: %v", err)
	}

	body := func(title, cfg string) string {
		b := staticChallengeBody(title, "OSCTF{x}")
		return strings.TrimSuffix(b, "}") + `,"type":"validating-probe-type","type_config":` + cfg + `}`
	}

	// --- Reachability + author-time reject: a create whose type_config is rejected fails 422 with a
	// per-field error. If ValidateConfig were not dialed on create, this would wrongly 201. ---
	if rec := do(t, srv, admin, http.MethodPost, "/api/v0/admin/challenges", body("Bad", `{"pattern":""}`)); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("create with a rejected type_config = %d (%s); want 422", rec.Code, rec.Body)
	} else if !fieldErrored(rec.Body.Bytes(), "type_config.pattern") {
		t.Errorf("422 does not carry a type_config.pattern field error: %s", rec.Body)
	}

	// --- Good config: created, and the STORED type_config is the plugin's normalized (trimmed) form. ---
	id, _ := createChallenge(t, srv, admin, body("Good", `{"pattern":"  flag-123  "}`))
	if got := adminTypeConfig(t, srv, admin, id); got["pattern"] != "flag-123" {
		t.Errorf("stored type_config is not the normalized form: got %q, want %q", got["pattern"], "flag-123")
	}

	// --- Fail closed only on a config change: with the checker down, changing type_config is
	// refused (503), but an unrelated edit (title) still succeeds — a down plugin must not block it. ---
	down.Store(true)
	if rec := do(t, srv, admin, http.MethodPatch, "/api/v0/admin/challenges/"+id, `{"type_config":{"pattern":"new"}}`); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("config edit while the checker is down = %d (%s); want 503 (fail closed)", rec.Code, rec.Body)
	}
	if rec := do(t, srv, admin, http.MethodPatch, "/api/v0/admin/challenges/"+id, `{"title":"Renamed While Down"}`); rec.Code != http.StatusOK {
		t.Fatalf("title-only edit while the checker is down = %d (%s); want 200 (no dial for a non-config edit)", rec.Code, rec.Body)
	}
}

func fieldErrored(body []byte, field string) bool {
	var p struct {
		Errors map[string][]string `json:"errors"`
	}
	_ = json.Unmarshal(body, &p)
	_, ok := p.Errors[field]
	return ok
}

func adminTypeConfig(t *testing.T, srv http.Handler, admin *cookieJar, id string) map[string]string {
	t.Helper()
	rec := do(t, srv, admin, http.MethodGet, "/api/v0/admin/challenges/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET challenge = %d (%s)", rec.Code, rec.Body)
	}
	var out struct {
		TypeConfig map[string]string `json:"type_config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode challenge: %v", err)
	}
	return out.TypeConfig
}
