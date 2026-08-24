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
