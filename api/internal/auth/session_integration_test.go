//go:build integration

package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/osctf/platform/internal/auth"
	"github.com/osctf/platform/internal/testsupport"
)

func TestSessionStoreIntegration(t *testing.T) {
	rdb := testsupport.Redis(t)
	store := auth.NewSessionStore(rdb, time.Hour)
	ctx := context.Background()
	uid := uuid.New()

	s1, err := store.Create(ctx, uid, "user", "1.2.3.4", "ua")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.Get(ctx, s1.Token)
	if err != nil || got.UserID != uid {
		t.Fatalf("Get: %v, user=%v", err, got.UserID)
	}

	// A second session, then revoke all except s1 (self password-change semantics).
	s2, _ := store.Create(ctx, uid, "user", "1.2.3.4", "ua")
	if err := store.DeleteAllForUser(ctx, uid, s1.Token); err != nil {
		t.Fatalf("DeleteAllForUser: %v", err)
	}
	if _, err := store.Get(ctx, s2.Token); err != auth.ErrNoSession {
		t.Errorf("s2 should be revoked, got %v", err)
	}
	if _, err := store.Get(ctx, s1.Token); err != nil {
		t.Errorf("s1 should survive, got %v", err)
	}

	// Delete s1 → gone.
	if err := store.Delete(ctx, s1.Token); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := store.Get(ctx, s1.Token); err != auth.ErrNoSession {
		t.Errorf("s1 should be gone, got %v", err)
	}
}

// TestSessionIndexSurvivesSetDriftIntegration covers 2f-v scenario 2: the sess:user
// index expired while an active session lived on. A Get on the live session must
// re-index it, so a later ban-time DeleteAllForUser still finds and revokes it —
// otherwise a banned user would keep a live session indefinitely.
func TestSessionIndexSurvivesSetDriftIntegration(t *testing.T) {
	rdb := testsupport.Redis(t)
	const ttl = time.Hour
	store := auth.NewSessionStore(rdb, ttl)
	ctx := context.Background()
	uid := uuid.New()

	s, err := store.Create(ctx, uid, "user", "ip", "ua")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Drift: the user set is gone, and the session's sliding TTL dropped below half
	// (so the next Get refreshes and must re-index).
	setKey := "sess:user:" + uid.String()
	if err := rdb.Del(ctx, setKey).Err(); err != nil {
		t.Fatalf("del set: %v", err)
	}
	if err := rdb.Expire(ctx, "sess:"+s.Token, ttl/4).Err(); err != nil {
		t.Fatalf("expire session: %v", err)
	}

	if _, err := store.Get(ctx, s.Token); err != nil {
		t.Fatalf("Get on live session: %v", err)
	}
	if n, _ := rdb.SCard(ctx, setKey).Result(); n != 1 {
		t.Fatalf("index not repopulated on Get: %d members, want 1", n)
	}

	if err := store.DeleteAllForUser(ctx, uid, ""); err != nil {
		t.Fatalf("DeleteAllForUser: %v", err)
	}
	if _, err := store.Get(ctx, s.Token); err != auth.ErrNoSession {
		t.Errorf("session survived revocation despite the drift fix: %v", err)
	}
}
