//go:build integration

package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/swayyaam/OSCTF/internal/auth"
	"github.com/swayyaam/OSCTF/internal/testsupport"
)

// The login state is SINGLE-USE. This is what stops a captured callback URL being replayed: the
// first use consumes it, and every later use looks exactly like an unknown state.
func TestLoginStateIsSingleUse(t *testing.T) {
	rdb := testsupport.Redis(t)
	store := auth.NewLoginStateStore(rdb, auth.LoginStateTTL)
	ctx := context.Background()

	token, err := store.Mint()
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := store.Store(ctx, token, "oidc", "provider-side-state"); err != nil {
		t.Fatalf("store: %v", err)
	}

	got, err := store.Consume(ctx, token)
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got.Provider != "oidc" || got.ProviderState != "provider-side-state" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if _, err := store.Consume(ctx, token); !errors.Is(err, auth.ErrNoLoginState) {
		t.Fatalf("second consume returned %v; a state must be usable exactly once", err)
	}
}

// An unknown or empty state is refused, and refused the SAME way a consumed one is — a caller
// cannot tell "never issued" from "already used".
func TestLoginStateUnknownIsRefused(t *testing.T) {
	rdb := testsupport.Redis(t)
	store := auth.NewLoginStateStore(rdb, auth.LoginStateTTL)
	ctx := context.Background()

	for _, token := range []string{"", "not-a-real-state"} {
		if _, err := store.Consume(ctx, token); !errors.Is(err, auth.ErrNoLoginState) {
			t.Errorf("Consume(%q) = %v, want ErrNoLoginState", token, err)
		}
	}
}

// Two logins started at once get distinct states, so consuming one cannot invalidate the other.
func TestLoginStatesAreIndependent(t *testing.T) {
	rdb := testsupport.Redis(t)
	store := auth.NewLoginStateStore(rdb, auth.LoginStateTTL)
	ctx := context.Background()

	a, err := store.Mint()
	if err != nil {
		t.Fatalf("mint a: %v", err)
	}
	if err := store.Store(ctx, a, "oidc", "a"); err != nil {
		t.Fatalf("store a: %v", err)
	}
	b, err := store.Mint()
	if err != nil {
		t.Fatalf("mint b: %v", err)
	}
	if err := store.Store(ctx, b, "oidc", "b"); err != nil {
		t.Fatalf("store b: %v", err)
	}
	if a == b {
		t.Fatal("two logins received the same state token")
	}
	if _, err := store.Consume(ctx, a); err != nil {
		t.Fatalf("consume a: %v", err)
	}
	if _, err := store.Consume(ctx, b); err != nil {
		t.Fatalf("consuming a's state invalidated b: %v", err)
	}
}
