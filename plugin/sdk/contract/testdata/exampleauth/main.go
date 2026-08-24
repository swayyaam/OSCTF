// Command exampleauth is a minimal redirect auth plugin used by the contract-harness test — the
// smallest honest example of the auth author surface: implement sdk.RedirectAuth (Info, Begin,
// Complete), call sdk.Serve. Nothing else — no go-plugin, no gRPC, no protobuf.
//
// Two things it demonstrates that an auth author must get right, because the host enforces both:
//
//  1. Begin uses the state the HOST supplied, verbatim, as the authorize URL's state parameter.
//     Inventing one means the host refuses to start the login at all — it verifies the URL before
//     setting any cookie. The value returned in BeginRedirect.State is something different: the
//     plugin's OWN round-trip data (a PKCE verifier, a nonce), which never reaches the browser.
//
//  2. Complete returns an ERROR when it cannot decide. Returning a bare Identity would be read by
//     the host as a successful authentication — for an auth plugin that is the difference between
//     "the login failed" and "anyone who can reach the callback is signed in".
//
// It sets EmailVerified deliberately: the host will not bind a login to an existing account by
// address unless the provider says it verified that address.
package main

import (
	"errors"
	"net/url"

	"github.com/swayyaam/OSCTF/plugin/sdk"
)

type provider struct{}

func (provider) Info() sdk.Info { return sdk.Info{Name: "example-oidc", Version: "0.1.0"} }

// Begin builds the authorize URL. The host's state goes in unchanged.
func (provider) Begin(state, redirectURI string) (sdk.BeginRedirect, error) {
	if state == "" {
		return sdk.BeginRedirect{}, errors.New("host supplied no state")
	}
	u := "https://idp.example.test/authorize?client_id=example" +
		"&redirect_uri=" + url.QueryEscape(redirectURI) +
		"&state=" + url.QueryEscape(state)
	// The returned State is the plugin's own round-trip data, NOT the CSRF state.
	return sdk.BeginRedirect{AuthorizeURL: u, State: "pkce-verifier-and-nonce"}, nil
}

// Complete turns the callback into an identity, or an error if it cannot.
func (provider) Complete(roundTrip string, params map[string]string) (sdk.Identity, error) {
	if roundTrip == "" {
		return sdk.Identity{}, errors.New("missing round-trip state")
	}
	code := params["code"]
	if code == "" {
		// Cannot decide who this is. An error, never an empty-but-successful identity.
		return sdk.Identity{}, errors.New("callback carried no authorization code")
	}
	return sdk.Identity{
		Subject:       "example-subject-" + code,
		Email:         "player@example.test",
		Username:      "player",
		EmailVerified: true,
	}, nil
}

func main() { sdk.Serve(sdk.Auth, provider{}) }
