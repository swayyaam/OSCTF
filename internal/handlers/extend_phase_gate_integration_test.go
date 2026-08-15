//go:build integration

package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/osctf/platform/internal/testsupport"
)

// TestExtendRejectedAfterEventEnd (3a-ix): once the event has ended, extending a
// still-live per-team instance must be rejected — not silently prolong it. Instance TTL
// is not itself capped at the event end (CleanupEnded reclaims per-team instances when
// the event ends), so an ungated extend during the cleanup window pushes the expiry
// forward and can outlive the event if the one-shot cleanup already swept the team. The
// gate makes extend a running-phase operation; here the expiry must not move.
func TestExtendRejectedAfterEventEnd(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := schedulerServer(t, pool, rdb, 3)

	admin := makeAdmin(t, srv, pool, "root", "root@example.com")
	_, slug := createChallenge(t, srv, admin, perTeamChallenge)
	player := teamUp(t, srv, "player", "player@example.com", "Players")
	base := "/api/v0/challenges/" + slug + "/instance"

	// Start a live instance while the event is running.
	rec := do(t, srv, player, http.MethodPost, base, "")
	if rec.Code != http.StatusCreated {
		t.Fatalf("start = %d (%s)", rec.Code, rec.Body)
	}
	expiryBefore := instanceExpiry(t, srv, player, slug)
	if expiryBefore == "" {
		t.Fatal("started instance has no expiry to guard")
	}

	// End the event, leaving the instance live (cleanup has not run yet).
	if _, err := pool.Exec(context.Background(),
		`UPDATE events SET starts_at=$1, ends_at=$2`,
		time.Now().Add(-2*time.Hour), time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("end event: %v", err)
	}

	// Extend must be rejected now that the event is over.
	if rec := do(t, srv, player, http.MethodPost, base+"/extend", ""); rec.Code != http.StatusConflict {
		t.Fatalf("extend after event end = %d (%s), want 409", rec.Code, rec.Body)
	}

	// The expiry must be untouched — extend did not prolong the instance past the event.
	if after := instanceExpiry(t, srv, player, slug); after != expiryBefore {
		t.Errorf("expiry moved after a rejected extend: %s -> %s", expiryBefore, after)
	}
}

// instanceExpiry reads the caller team's instance expiry from the challenge detail.
func instanceExpiry(t *testing.T, srv http.Handler, jar *cookieJar, slug string) string {
	t.Helper()
	rec := do(t, srv, jar, http.MethodGet, "/api/v0/challenges/"+slug, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get challenge detail = %d (%s)", rec.Code, rec.Body)
	}
	var detail struct {
		Instance *struct {
			ExpiresAt string `json:"expires_at"`
		} `json:"instance"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Instance == nil {
		return ""
	}
	return detail.Instance.ExpiresAt
}
