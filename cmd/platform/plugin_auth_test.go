package main

import (
	"testing"

	"github.com/swayyaam/OSCTF/internal/auth"
)

// The redirect capability is expressed by the TYPE, not by a boolean a caller must remember to
// check. A password-only plugin must not satisfy auth.RedirectProvider, so the route's type
// assertion is a real capability check rather than a hopeful one — if this ever passes for the
// password-only shape, every password plugin becomes reachable on the redirect routes.
func TestRedirectCapabilityIsStructural(t *testing.T) {
	passwordOnly := pluginAuthProvider{name: "pw", password: true}
	if _, ok := any(passwordOnly).(auth.RedirectProvider); ok {
		t.Fatal("a password-only provider satisfies auth.RedirectProvider; the redirect routes would accept it")
	}
	if _, ok := any(passwordOnly).(auth.AuthProvider); !ok {
		t.Fatal("a password-only provider must still satisfy auth.AuthProvider")
	}

	redirectCapable := pluginRedirectAuthProvider{pluginAuthProvider: pluginAuthProvider{name: "oidc"}}
	if _, ok := any(redirectCapable).(auth.RedirectProvider); !ok {
		t.Fatal("a redirect-capable provider must satisfy auth.RedirectProvider")
	}
	if _, ok := any(redirectCapable).(auth.AuthProvider); !ok {
		t.Fatal("a redirect-capable provider must also satisfy auth.AuthProvider")
	}
}

// A provider without the password capability rejects a credential instead of dialling the
// plugin. The resolver is nil here on purpose: reaching it would panic, so this also proves the
// capability check happens BEFORE any call.
func TestProviderWithoutPasswordCapabilityRejects(t *testing.T) {
	p := pluginAuthProvider{name: "redirect-only", password: false}
	id, err := p.Authenticate(t.Context(), "someone@example.test", "hunter2")
	if err == nil {
		t.Fatalf("got user %v, want a rejection", id)
	}
	if err != auth.ErrInvalidCredentials {
		t.Fatalf("got %v, want auth.ErrInvalidCredentials", err)
	}
}
