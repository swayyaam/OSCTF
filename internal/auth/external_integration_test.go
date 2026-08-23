//go:build integration

package auth_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/swayyaam/OSCTF/internal/auth"
	"github.com/swayyaam/OSCTF/internal/db/gen"
	"github.com/swayyaam/OSCTF/internal/testsupport"
)

// The auth return-path contract, against a real database. Each test names the guarantee it
// pins; see docs/v0.3/04-plugin-interfaces.md and internal/auth/external.go.

func resolverFor(t *testing.T, policy auth.ProvisionPolicy) (*auth.ExternalResolver, *gen.Queries) {
	t.Helper()
	pool, _ := testsupport.Postgres(t)
	q := gen.New(pool)
	return auth.NewExternalResolver(q, policy, slog.New(slog.NewTextHandler(io.Discard, nil))), q
}

var userSeq int

// mkUser creates a local account the way registration would, so the tests exercise binding
// against ordinary users rather than specially-shaped ones.
func mkUser(t *testing.T, q *gen.Queries, email string, banned bool) gen.User {
	t.Helper()
	userSeq++
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	u, err := q.CreateUser(context.Background(), gen.CreateUserParams{
		ID: id, Username: fmt.Sprintf("local%d", userSeq), Email: email,
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHQ$aGFzaA", Role: "user",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if banned {
		yes := true
		if _, err := q.UpdateUserAdminFields(context.Background(), gen.UpdateUserAdminFieldsParams{ID: u.ID, Banned: &yes}); err != nil {
			t.Fatalf("ban user: %v", err)
		}
	}
	return u
}

func verified(subject, email string) auth.ExternalIdentity {
	return auth.ExternalIdentity{Subject: subject, Email: email, EmailVerified: true}
}

// GUARANTEE: an unverified email NEVER attaches a login to an existing account, under any
// policy. This is the account-takeover guard — without it, a provider that lets a user assert
// any address owns every account whose address is guessable.
func TestExternalUnverifiedEmailNeverBinds(t *testing.T) {
	for _, policy := range []auth.ProvisionPolicy{auth.ProvisionOpen, auth.ProvisionInviteOnly, auth.ProvisionOff} {
		t.Run(string(policy), func(t *testing.T) {
			r, q := resolverFor(t, policy)
			victim := mkUser(t, q, fmt.Sprintf("victim-%s@example.test", policy), false)

			id := auth.ExternalIdentity{Subject: "attacker-subject", Email: victim.Email, EmailVerified: false}
			got, err := r.Resolve(context.Background(), "oidc", id)
			if !errors.Is(err, auth.ErrExternalRejected) {
				t.Fatalf("resolved to %v (err %v); an unverified email must never bind", got.ID, err)
			}
		})
	}
}

// GUARANTEE: a verified email binds to an existing account under open and invite-only, and is
// refused under off. This is the whole point of invite-only — an admin creates the account, SSO
// attaches to it.
func TestExternalVerifiedEmailBindsPerPolicy(t *testing.T) {
	cases := map[auth.ProvisionPolicy]bool{
		auth.ProvisionOpen:       true,
		auth.ProvisionInviteOnly: true,
		auth.ProvisionOff:        false,
	}
	for policy, wantBind := range cases {
		t.Run(string(policy), func(t *testing.T) {
			r, q := resolverFor(t, policy)
			existing := mkUser(t, q, fmt.Sprintf("member-%s@example.test", policy), false)

			got, err := r.Resolve(context.Background(), "oidc", verified("subj-"+string(policy), existing.Email))
			if !wantBind {
				if !errors.Is(err, auth.ErrExternalRejected) {
					t.Fatalf("policy %s: got %v/%v, want rejection", policy, got.ID, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("policy %s: %v", policy, err)
			}
			if got.ID != existing.ID {
				t.Fatalf("policy %s: bound to %v, want the existing account %v", policy, got.ID, existing.ID)
			}
		})
	}
}

// GUARANTEE: only `open` creates an account, and a provisioned account is ALWAYS the lowest
// role. A plugin cannot mint an admin.
func TestExternalProvisioningOnlyUnderOpenAndAlwaysLowestRole(t *testing.T) {
	cases := map[auth.ProvisionPolicy]bool{
		auth.ProvisionOpen:       true,
		auth.ProvisionInviteOnly: false,
		auth.ProvisionOff:        false,
	}
	for policy, wantCreate := range cases {
		t.Run(string(policy), func(t *testing.T) {
			r, _ := resolverFor(t, policy)
			id := verified("newsubj-"+string(policy), fmt.Sprintf("newcomer-%s@example.test", policy))
			id.Username = "Newcomer"

			got, err := r.Resolve(context.Background(), "oidc", id)
			if !wantCreate {
				if !errors.Is(err, auth.ErrExternalRejected) {
					t.Fatalf("policy %s: created %v (err %v); only open may provision", policy, got.ID, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("policy %s: %v", policy, err)
			}
			if got.Role != "user" {
				t.Fatalf("provisioned role %q, want the lowest role %q", got.Role, "user")
			}
			if got.Banned || got.Hidden {
				t.Fatalf("provisioned account should be an ordinary participant, got banned=%v hidden=%v", got.Banned, got.Hidden)
			}
		})
	}
}

// GUARANTEE: the binding is reused. A second login for the same subject resolves to the same
// account rather than creating another, and does so even after the provider stops asserting
// verification — the binding, not the claim, is what the core trusts.
func TestExternalBindingIsReusedAndSurvivesUnverifiedLaterClaims(t *testing.T) {
	r, _ := resolverFor(t, auth.ProvisionOpen)
	ctx := context.Background()

	first, err := r.Resolve(ctx, "oidc", verified("stable-subject", "stable@example.test"))
	if err != nil {
		t.Fatalf("first login: %v", err)
	}
	second, err := r.Resolve(ctx, "oidc", verified("stable-subject", "stable@example.test"))
	if err != nil {
		t.Fatalf("second login: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("second login produced %v, want the same account %v", second.ID, first.ID)
	}
	// Same subject, now unverified and with a different address: the binding decides.
	third, err := r.Resolve(ctx, "oidc", auth.ExternalIdentity{Subject: "stable-subject", Email: "elsewhere@example.test"})
	if err != nil {
		t.Fatalf("bound login with an unverified claim should still resolve: %v", err)
	}
	if third.ID != first.ID {
		t.Fatalf("bound identity resolved to %v, want %v", third.ID, first.ID)
	}
}

// GUARANTEE: a banned account cannot log in through a provider, whether it is reached by an
// existing binding or by a verified email match. Bans gate this path exactly as they gate a
// password login.
func TestExternalBannedAccountRejected(t *testing.T) {
	ctx := context.Background()

	t.Run("via verified email", func(t *testing.T) {
		r, q := resolverFor(t, auth.ProvisionOpen)
		banned := mkUser(t, q, "banned-match@example.test", true)
		if _, err := r.Resolve(ctx, "oidc", verified("bm-subject", banned.Email)); !errors.Is(err, auth.ErrExternalRejected) {
			t.Fatalf("got %v, want rejection for a banned account", err)
		}
	})

	t.Run("via existing binding", func(t *testing.T) {
		r, q := resolverFor(t, auth.ProvisionOpen)
		u, err := r.Resolve(ctx, "oidc", verified("bb-subject", "banned-later@example.test"))
		if err != nil {
			t.Fatalf("provision: %v", err)
		}
		yes := true
		if _, err := q.UpdateUserAdminFields(ctx, gen.UpdateUserAdminFieldsParams{ID: u.ID, Banned: &yes}); err != nil {
			t.Fatalf("ban: %v", err)
		}
		if _, err := r.Resolve(ctx, "oidc", verified("bb-subject", "banned-later@example.test")); !errors.Is(err, auth.ErrExternalRejected) {
			t.Fatalf("got %v, want rejection after the bound account was banned", err)
		}
	})
}

// GUARANTEE: a username already taken does not block provisioning, and never yields a duplicate.
func TestExternalProvisioningDerivesAFreeUsername(t *testing.T) {
	r, q := resolverFor(t, auth.ProvisionOpen)
	ctx := context.Background()

	taken := mkUser(t, q, "taken@example.test", false)
	id := verified("collide-subject", "collide@example.test")
	id.Username = taken.Username // ask for a username that already exists

	got, err := r.Resolve(ctx, "oidc", id)
	if err != nil {
		t.Fatalf("provision under username collision: %v", err)
	}
	if got.Username == taken.Username {
		t.Fatalf("provisioned duplicate username %q", got.Username)
	}
	if got.ID == taken.ID {
		t.Fatalf("provisioning resolved to the existing account; it must create a new one")
	}
}
