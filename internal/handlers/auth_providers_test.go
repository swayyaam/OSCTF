package handlers

import "testing"

// The provider callback URL is built from the CONFIGURED public origin, never from the incoming
// request's Host. A provider that echoed an attacker-supplied host back would otherwise be told
// to deliver the authorization code somewhere the operator never configured.
func TestCallbackURLComesFromConfiguredOrigin(t *testing.T) {
	cases := map[string]string{
		"https://ctf.example.test":  "https://ctf.example.test/api/v1/auth/oidc/callback",
		"https://ctf.example.test/": "https://ctf.example.test/api/v1/auth/oidc/callback",
		"http://localhost:8080":     "http://localhost:8080/api/v1/auth/oidc/callback",
	}
	for base, want := range cases {
		s := &Server{d: Deps{BaseURL: base}}
		if got := s.callbackURL("oidc"); got != want {
			t.Errorf("BaseURL %q: callbackURL = %q, want %q", base, got, want)
		}
	}
}

// The host mints the login state and REQUIRES the provider to have used it. Without this check
// the core would be trusting a plugin to protect the login against CSRF — which is the one thing
// the auth return-path contract says not to do.
//
// This is the guard for a bug that shipped and could not have been caught by any existing test:
// the host used to call Begin FIRST and mint its state afterwards, so the state the identity
// provider echoed back was the plugin's, never the host's, and every external login failed the
// cookie comparison. No test saw it because no redirect-capable plugin existed yet.
func TestAuthorizeURLMustCarryTheHostState(t *testing.T) {
	const want = "host-minted-state"
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"carries the host state", "https://idp.test/authorize?client_id=x&state=" + want, true},
		{"state among other params", "https://idp.test/authorize?state=" + want + "&scope=openid", true},
		{"provider substituted its own", "https://idp.test/authorize?state=plugin-chosen", false},
		{"no state at all", "https://idp.test/authorize?client_id=x", false},
		{"empty url", "", false},
		{"unparseable", "://not a url", false},
		{"state is a prefix only", "https://idp.test/authorize?state=" + want + "-extra", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := authorizeURLCarriesState(tc.url, want)
			if tc.ok && err != nil {
				t.Fatalf("want accepted, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("want refused, got accepted")
			}
		})
	}
}
