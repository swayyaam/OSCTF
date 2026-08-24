// An OIDC/OAuth2 auth plugin for OSCTF: the reference implementation of the `redirect`
// capability. It fronts any standards-compliant OpenID Connect provider (Keycloak, Auth0, Okta,
// Google, GitHub Enterprise via OIDC, Dex, …) discovered from its issuer URL.
//
// What this plugin is trusted with, and what it is not:
//
// It asserts an identity. It does NOT decide what that identity means. The host maps the
// assertion to a local account under its own provisioning policy, always at the lowest role, and
// ignores anything role-shaped in the claims. See the auth return-path contract in the platform's
// docs/v0.3/04-plugin-interfaces.md — reading it is worth the five minutes before trusting any
// auth plugin, including this one.
//
// Protocol choices, none of them optional:
//   - Authorization Code flow with PKCE (S256). Never the implicit flow.
//   - The CSRF `state` is the HOST's, used verbatim; this plugin does not invent one.
//   - The ID token's signature, issuer, audience, and expiry are verified by go-oidc against the
//     provider's published JWKS. A `nonce` this plugin generated must match.
//   - email_verified is reported truthfully from the provider's claim, never assumed. The host
//     refuses to bind a login to an existing account on an unverified address.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/swayyaam/OSCTF/plugin/sdk"
)

func main() { sdk.Serve(sdk.Auth, &provider{}) }

// provider implements sdk.RedirectAuth. It does NOT implement sdk.PasswordAuth: an OIDC provider
// authenticates at the identity provider, so this plugin never sees a user's password. That is
// deliberate and visible in the type — the host derives capabilities from what is implemented, so
// this plugin cannot be routed a plaintext credential even by mistake.
type provider struct {
	once sync.Once
	// discovery is resolved lazily on first use rather than at start-up: an identity provider that
	// is briefly unreachable should delay a login, not prevent the plugin from loading and leave
	// the host with a permanently failed plugin.
	discovered *oidc.Provider
	discErr    error
}

func (p *provider) Info() sdk.Info { return sdk.Info{Name: "oidc", Version: "0.1.0"} }

// discoveryTimeout bounds the well-known fetch. A login is a user waiting on a page.
const discoveryTimeout = 10 * time.Second

func (p *provider) issuer() string   { return strings.TrimSpace(sdk.Config().String("issuer")) }
func (p *provider) clientID() string { return strings.TrimSpace(sdk.Config().String("client_id")) }
func (p *provider) secret() string   { return sdk.Config().String("client_secret") }

// scopes returns the requested scopes. openid is mandatory; email is what the host needs to bind
// a login to an existing account, so both are always present regardless of configuration.
func (p *provider) scopes() []string {
	want := []string{oidc.ScopeOpenID, "email", "profile"}
	if extra := strings.TrimSpace(sdk.Config().String("scopes")); extra != "" {
		for _, s := range strings.Split(extra, ",") {
			if s = strings.TrimSpace(s); s != "" && !contains(want, s) {
				want = append(want, s)
			}
		}
	}
	return want
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func (p *provider) resolve(ctx context.Context) (*oidc.Provider, error) {
	p.once.Do(func() {
		iss := p.issuer()
		if iss == "" {
			p.discErr = errors.New("oidc: no issuer configured")
			return
		}
		dctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
		defer cancel()
		p.discovered, p.discErr = oidc.NewProvider(dctx, iss)
		if p.discErr != nil {
			// Reset so a later login retries discovery: a provider that was down at first use
			// should not poison this plugin for its whole lifetime.
			p.once = sync.Once{}
		}
	})
	return p.discovered, p.discErr
}

func (p *provider) oauthConfig(prov *oidc.Provider, redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.clientID(),
		ClientSecret: p.secret(),
		Endpoint:     prov.Endpoint(),
		RedirectURL:  redirectURI,
		Scopes:       p.scopes(),
	}
}

// roundTrip is what this plugin needs on the callback and cannot recompute: the PKCE verifier and
// the nonce it generated. The host stores it with the login and hands it back to Complete. It is
// NOT the CSRF state and never reaches the browser or the identity provider.
type roundTrip struct {
	Verifier    string `json:"v"`
	Nonce       string `json:"n"`
	RedirectURI string `json:"r"`
}

// Begin builds the authorize URL. `state` is the HOST's CSRF value and is used verbatim — the
// host verifies the returned URL carries exactly it and refuses the login otherwise, so a plugin
// substituting its own value cannot start a login at all.
func (p *provider) Begin(state, redirectURI string) (sdk.BeginRedirect, error) {
	ctx := context.Background()
	prov, err := p.resolve(ctx)
	if err != nil {
		return sdk.BeginRedirect{}, fmt.Errorf("oidc: provider discovery: %w", err)
	}
	if p.clientID() == "" {
		return sdk.BeginRedirect{}, errors.New("oidc: no client_id configured")
	}

	verifier, err := randomURLSafe(32)
	if err != nil {
		return sdk.BeginRedirect{}, err
	}
	nonce, err := randomURLSafe(32)
	if err != nil {
		return sdk.BeginRedirect{}, err
	}

	// PKCE S256. `plain` is not offered: a provider that only supports plain would silently
	// weaken the flow, and failing is the honest outcome.
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	authURL := p.oauthConfig(prov, redirectURI).AuthCodeURL(state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", challenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)

	blob, err := json.Marshal(roundTrip{Verifier: verifier, Nonce: nonce, RedirectURI: redirectURI})
	if err != nil {
		return sdk.BeginRedirect{}, fmt.Errorf("oidc: encoding round-trip state: %w", err)
	}
	return sdk.BeginRedirect{
		AuthorizeURL: authURL,
		State:        base64.RawURLEncoding.EncodeToString(blob),
	}, nil
}

// Complete exchanges the code and verifies the ID token. Every failure returns an error: an auth
// plugin that cannot decide MUST NOT return a bare identity, because the host would treat it as a
// successful authentication.
func (p *provider) Complete(state string, params map[string]string) (sdk.Identity, error) {
	ctx := context.Background()

	// A provider that redirects back with an error says so in the query. Surface it rather than
	// failing later on a missing code, so the operator's log names the real cause.
	if e := params["error"]; e != "" {
		return sdk.Identity{}, fmt.Errorf("oidc: provider returned error %q: %s", e, params["error_description"])
	}
	code := params["code"]
	if code == "" {
		return sdk.Identity{}, errors.New("oidc: callback carried no authorization code")
	}

	blob, err := base64.RawURLEncoding.DecodeString(state)
	if err != nil {
		return sdk.Identity{}, errors.New("oidc: round-trip state is not decodable")
	}
	var rt roundTrip
	if err := json.Unmarshal(blob, &rt); err != nil {
		return sdk.Identity{}, errors.New("oidc: round-trip state is not readable")
	}

	prov, err := p.resolve(ctx)
	if err != nil {
		return sdk.Identity{}, fmt.Errorf("oidc: provider discovery: %w", err)
	}
	cfg := p.oauthConfig(prov, rt.RedirectURI)

	tok, err := cfg.Exchange(ctx, code, oauth2.SetAuthURLParam("code_verifier", rt.Verifier))
	if err != nil {
		return sdk.Identity{}, fmt.Errorf("oidc: exchanging the authorization code: %w", err)
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		// No ID token means this was a plain OAuth2 response, not OIDC. Refuse: the userinfo
		// endpoint alone does not prove who authenticated.
		return sdk.Identity{}, errors.New("oidc: token response carried no id_token")
	}

	// Signature, issuer, audience, and expiry, against the provider's published JWKS.
	idTok, err := prov.Verifier(&oidc.Config{ClientID: p.clientID()}).Verify(ctx, rawID)
	if err != nil {
		return sdk.Identity{}, fmt.Errorf("oidc: verifying the id_token: %w", err)
	}
	if idTok.Nonce != rt.Nonce {
		// A replayed or injected token. The nonce is what ties this ID token to the authorize
		// request this plugin actually made.
		return sdk.Identity{}, errors.New("oidc: id_token nonce does not match the login it answers")
	}

	var claims struct {
		Subject       string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified"`
		Preferred     string `json:"preferred_username"`
		Name          string `json:"name"`
	}
	if err := idTok.Claims(&claims); err != nil {
		return sdk.Identity{}, fmt.Errorf("oidc: reading id_token claims: %w", err)
	}
	if claims.Subject == "" {
		return sdk.Identity{}, errors.New("oidc: id_token carries no subject")
	}

	// email_verified is reported as the provider stated it. A provider that omits the claim has
	// NOT asserted verification, so this is false — the host then refuses to bind the login to an
	// existing account by address, which is the safe reading of silence.
	verified := claims.EmailVerified != nil && *claims.EmailVerified

	username := claims.Preferred
	if username == "" {
		username = claims.Name
	}

	// No claims are forwarded. The host rejects a login whose claims carry role/admin/user_id, and
	// passing through an arbitrary provider's claim set is the easiest way to trip that by
	// accident — a provider that happens to emit a "roles" claim would break every login.
	return sdk.Identity{
		Subject:       claims.Subject,
		Email:         strings.TrimSpace(claims.Email),
		Username:      username,
		EmailVerified: verified,
	}, nil
}

func randomURLSafe(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oidc: generating random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
