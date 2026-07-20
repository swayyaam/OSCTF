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
