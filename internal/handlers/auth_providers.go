package handlers

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/swayyaam/OSCTF/internal/apigen"
	"github.com/swayyaam/OSCTF/internal/apperr"
	"github.com/swayyaam/OSCTF/internal/auth"
	"github.com/swayyaam/OSCTF/internal/httpx"
)

// External (redirect) login. The core owns every security-relevant part of this flow: it mints
// and verifies the state, decides what the returned identity is allowed to mean (via
// auth.ExternalResolver), and issues the session. The plugin's only job is to talk to its
// identity source. See the auth return-path contract in docs/v0.3/04-plugin-interfaces.md.

const (
	// loginSuccessPath is where a completed login lands. The session cookie is already set.
	loginSuccessPath = "/"
	// loginFailurePath is where every failure lands. The reason is deliberately NOT in the URL:
	// a redirect target is not a place to explain whether an account exists, is banned, or was
	// refused by policy. Operators get the reason in the log instead.
	loginFailurePath = "/login?error=sso"
)

// ListAuthProviders reports the login methods available right now. Plugin providers appear only
// while their plugin is registered, so a client that renders from this never offers a button
// that cannot work.
func (s *Server) ListAuthProviders(ctx context.Context, _ apigen.ListAuthProvidersRequestObject) (apigen.ListAuthProvidersResponseObject, error) {
	if s.d.Auth == nil {
		return apigen.ListAuthProviders200JSONResponse{}, nil
	}
	builtin := s.d.Auth.Default().Name()
	out := make([]apigen.AuthProviderInfo, 0, 4)
	for _, name := range s.d.Auth.Names() {
		if name == builtin {
			// The built-in is a credential form, and it is hidden when disabled — an SSO-only
			// deployment should not advertise a login that returns 403.
			if s.d.EmailLoginDisabled {
				continue
			}
			out = append(out, apigen.AuthProviderInfo{Id: name, Redirect: false})
			continue
		}
		p, ok := s.d.Auth.Get(name)
		if !ok {
			continue
		}
		_, redirect := p.(auth.RedirectProvider)
		out = append(out, apigen.AuthProviderInfo{Id: name, Redirect: redirect})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return apigen.ListAuthProviders200JSONResponse(out), nil
}

// redirectProvider resolves a provider that can actually start a redirect login. A password-only
// provider does not satisfy auth.RedirectProvider, so this is a real capability check.
func (s *Server) redirectProvider(name string) (auth.RedirectProvider, bool) {
	if s.d.Auth == nil || s.d.LoginStates == nil || s.d.ExternalAuth == nil {
		return nil, false
	}
	p, ok := s.d.Auth.Get(name)
	if !ok {
		return nil, false
	}
	rp, ok := p.(auth.RedirectProvider)
	return rp, ok
}

// BeginProviderLogin starts an external login: ask the provider where to send the browser, mint
// our own state, bind it to this browser with a cookie, and redirect.
func (s *Server) BeginProviderLogin(ctx context.Context, request apigen.BeginProviderLoginRequestObject) (apigen.BeginProviderLoginResponseObject, error) {
	name := string(request.Provider)
	rp, ok := s.redirectProvider(name)
	if !ok {
		// Unknown provider and password-only provider are the same 404: which plugins are
		// installed is not something an unauthenticated caller needs to enumerate.
		return nil, apperr.ErrNotFound
	}
	if s.d.LoginIPBurst > 0 {
		if err := s.limit(ctx, "sso-begin", s.clientIP(ctx), s.d.LoginIPBurst, s.d.LoginIPWindow); err != nil {
			return nil, err
		}
	}

	authorizeURL, providerState, err := rp.Begin(ctx, s.callbackURL(name))
	if err != nil {
		// No fallback to another provider: a broken provider is a failed login, not a redirect
		// somewhere the user did not choose.
		s.log().Warn("external login could not start", "provider", name, "error", err.Error())
		return nil, &apperr.Unavailable{Detail: "the " + name + " login is unavailable right now"}
	}
	if authorizeURL == "" {
		s.log().Warn("external login returned no authorize URL", "provider", name)
		return nil, &apperr.Unavailable{Detail: "the " + name + " login is unavailable right now"}
	}

	state, err := s.d.LoginStates.Create(ctx, name, providerState)
	if err != nil {
		return nil, err
	}
	s.setLoginStateCookie(ctx, state)
	return apigen.BeginProviderLogin302Response{
		Headers: apigen.BeginProviderLogin302ResponseHeaders{Location: authorizeURL},
	}, nil
}

// CompleteProviderLogin finishes an external login. Every failure returns the SAME redirect: the
// browser is a poor error channel, and distinguishing "unknown provider" from "banned account"
// here would hand an attacker an oracle. The reason goes to the log.
func (s *Server) CompleteProviderLogin(ctx context.Context, request apigen.CompleteProviderLoginRequestObject) (apigen.CompleteProviderLoginResponseObject, error) {
	name := string(request.Provider)
	fail := func(reason string) (apigen.CompleteProviderLoginResponseObject, error) {
		s.log().Warn("external login failed", "provider", name, "reason", reason)
		s.clearLoginStateCookie(ctx)
		return apigen.CompleteProviderLogin302Response{
			Headers: apigen.CompleteProviderLogin302ResponseHeaders{Location: loginFailurePath},
		}, nil
	}

	rp, ok := s.redirectProvider(name)
	if !ok {
		return fail("no such redirect provider")
	}
	p, hp := httpx.HTTPFrom(ctx)
	if !hp {
		return fail("no request in context")
	}
	if s.d.LoginIPBurst > 0 {
		if err := s.limit(ctx, "sso-callback", s.clientIP(ctx), s.d.LoginIPBurst, s.d.LoginIPWindow); err != nil {
			return nil, err
		}
	}

	query := p.R.URL.Query()
	state := query.Get("state")

	// The state must match the cookie set when this login began, which proves the callback
	// reached the same browser. Without it a captured callback URL replays into a victim's
	// browser and silently signs them in as the attacker.
	cookie, cerr := p.R.Cookie(auth.LoginStateCookie)
	if cerr != nil || cookie.Value == "" {
		return fail("callback carried no state cookie")
	}
	if subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		return fail("state parameter does not match the bound cookie")
	}

	// Single use: consuming deletes it, so the same callback cannot be replayed.
	st, err := s.d.LoginStates.Consume(ctx, state)
	if err != nil {
		if errors.Is(err, auth.ErrNoLoginState) {
			return fail("state is unknown, expired, or already used")
		}
		return fail("state store unavailable")
	}
	if st.Provider != name {
		return fail("state was issued for a different provider")
	}

	params := make(map[string]string, len(query))
	for k := range query {
		params[k] = query.Get(k)
	}
	id, err := rp.Complete(ctx, st.ProviderState, params)
	if err != nil {
		return fail("the provider could not complete the login")
	}

	// The return path decides what the assertion is allowed to mean. It logs its own reason.
	u, err := s.d.ExternalAuth.Resolve(ctx, name, id)
	if err != nil {
		if errors.Is(err, auth.ErrExternalRejected) {
			return fail("the return path rejected the identity")
		}
		return fail("resolving the identity failed")
	}

	sess, err := s.d.Sessions.Create(ctx, u.ID, u.Role, s.clientIP(ctx), s.userAgent(ctx))
	if err != nil {
		return fail("creating the session failed")
	}
	// Close the ban-during-login race exactly as the password path does: a ban that committed
	// while we were talking to the provider would have revoked sessions before this one existed.
	if fresh, gerr := s.d.Users.Get(ctx, u.ID); gerr == nil && fresh.Banned {
		_ = s.d.Sessions.Delete(ctx, sess.Token)
		return fail("account was banned during login")
	}
	s.clearLoginStateCookie(ctx)
	s.setSessionCookie(ctx, sess.Token, s.d.SessionTTL)
	return apigen.CompleteProviderLogin302Response{
		Headers: apigen.CompleteProviderLogin302ResponseHeaders{Location: loginSuccessPath},
	}, nil
}

// callbackURL is the absolute redirect target handed to the provider. It is built from the
// configured public origin, never from the incoming request's Host — a provider that echoed an
// attacker-controlled host back would otherwise redirect the code somewhere else.
func (s *Server) callbackURL(provider string) string {
	return strings.TrimRight(s.d.BaseURL, "/") + "/api/v1/auth/" + provider + "/callback"
}

func (s *Server) setLoginStateCookie(ctx context.Context, state string) {
	p, ok := httpx.HTTPFrom(ctx)
	if !ok {
		return
	}
	//nolint:gosec // G124: Secure tracks the deployment scheme (OSCTF_BASE_URL), as for the session cookie.
	http.SetCookie(p.W, &http.Cookie{
		Name:     auth.LoginStateCookie,
		Value:    state,
		Path:     "/",
		HttpOnly: true,
		// Lax, not Strict: the callback is a cross-site GET from the provider, and Strict would
		// withhold the cookie exactly when it is needed. Lax still sends it on a top-level GET.
		SameSite: http.SameSiteLaxMode,
		Secure:   s.d.SecureCookies,
		MaxAge:   int(auth.LoginStateTTL.Seconds()),
	})
}

func (s *Server) clearLoginStateCookie(ctx context.Context) {
	p, ok := httpx.HTTPFrom(ctx)
	if !ok {
		return
	}
	//nolint:gosec // G124: see setLoginStateCookie.
	http.SetCookie(p.W, &http.Cookie{
		Name: auth.LoginStateCookie, Value: "", Path: "/", HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Secure: s.d.SecureCookies, MaxAge: -1,
	})
}

func (s *Server) log() *slog.Logger {
	if s.d.Log != nil {
		return s.d.Log
	}
	return slog.Default()
}
