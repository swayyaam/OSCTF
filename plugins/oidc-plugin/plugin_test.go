package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/swayyaam/OSCTF/plugin/sdk/contract"
)

// fakeIssuer serves just enough of an OpenID Provider for discovery to succeed: the well-known
// document. The contract run never exchanges a code, so no token endpoint is exercised — and that
// is the point of the check it does run, which is that the authorize URL is built correctly and
// that an incompletable callback is refused.
func fakeIssuer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			// go-oidc requires this to equal the URL it was given, so a mismatched issuer is
			// rejected — the same check that stops a hostile discovery document redirecting trust.
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keys":[]}`))
	})
	return srv
}

// TestPluginContract runs the SDK's auth contract against the built binary, exactly as the host
// would dial it. It asserts the handshake, the advertised type and capabilities, that the
// authorize URL carries the HOST's state verbatim, and that a callback with no authorization code
// is refused rather than answered with an identity.
func TestPluginContract(t *testing.T) {
	srv := fakeIssuer(t)
	t.Setenv("OSCTF_PLUGIN_CONFIG", fmt.Sprintf(
		`{"issuer":%q,"client_id":"osctf-test","client_secret":"test-secret"}`, srv.URL))

	contract.VerifyAuth(t, contract.Build(t, "."), contract.AuthCases{
		RedirectURI: "https://ctf.example.test/api/v1/auth/oidc/callback",
	})
}

// This plugin must NOT advertise the password capability: an OIDC login happens at the identity
// provider, so the plugin never sees a user's password. VerifyAuth checks the negative direction
// too (an unadvertised capability must fail closed), but state it here as an intention as well —
// adding a PasswordAuth method later would silently start routing plaintext credentials to it.
func TestPluginIsRedirectOnly(t *testing.T) {
	var p any = &provider{}
	if _, ok := p.(interface {
		Authenticate(identifier, secret string) (any, error)
	}); ok {
		t.Fatal("the OIDC plugin implements a password method; it must never receive a plaintext credential")
	}
}
