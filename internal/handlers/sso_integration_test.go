//go:build integration

package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/swayyaam/OSCTF/internal/audit"
	"github.com/swayyaam/OSCTF/internal/auth"
	"github.com/swayyaam/OSCTF/internal/clock"
	"github.com/swayyaam/OSCTF/internal/db/gen"
	"github.com/swayyaam/OSCTF/internal/events"
	"github.com/swayyaam/OSCTF/internal/handlers"
	"github.com/swayyaam/OSCTF/internal/httpserver"
	"github.com/swayyaam/OSCTF/internal/redisx"
	"github.com/swayyaam/OSCTF/internal/testsupport"
	"github.com/swayyaam/OSCTF/internal/users"
)

// The external-login chain end to end, through the real routes: Begin → state stored and bound to
// a cookie → the provider's callback → Complete → the return path → a session.
//
// Nothing had exercised this chain before. The unit tests cover its pieces and the contract tests
// cover a plugin in isolation; only this proves the host wires them together — which is how the
// state-ordering bug survived two green CI runs.

// fakeRedirect stands in for a redirect-capable auth plugin. Using a double rather than a real
// subprocess is deliberate: what is under test here is the HOST's chain, and a double can be made
// to misbehave in ways a correct plugin never would.
type fakeRedirect struct {
	name        string
	identity    auth.ExternalIdentity
	beginErr    error
	completeErr error
	// ignoreHostState makes Begin substitute its own state, which the host must refuse.
	ignoreHostState bool
	gotHostState    string
	gotProviderBlob string
}

func (f *fakeRedirect) Name() string { return f.name }

// Authenticate exists only to satisfy auth.AuthProvider; a redirect provider has no password.
func (f *fakeRedirect) Authenticate(context.Context, string, string) (uuid.UUID, error) {
	return uuid.Nil, auth.ErrInvalidCredentials
}

func (f *fakeRedirect) Begin(_ context.Context, hostState, _ string) (string, string, error) {
	if f.beginErr != nil {
		return "", "", f.beginErr
	}
	f.gotHostState = hostState
	used := hostState
	if f.ignoreHostState {
		used = "provider-invented-state"
	}
	return "https://idp.test/authorize?client_id=x&state=" + url.QueryEscape(used), "provider-blob", nil
}

func (f *fakeRedirect) Complete(_ context.Context, state string, _ map[string]string) (auth.ExternalIdentity, error) {
	f.gotProviderBlob = state
	if f.completeErr != nil {
		return auth.ExternalIdentity{}, f.completeErr
	}
	return f.identity, nil
}

func ssoServer(t *testing.T, pool *pgxpool.Pool, rdb *redis.Client, policy auth.ProvisionPolicy, fp *fakeRedirect) http.Handler {
	t.Helper()
	q := gen.New(pool)
	sessions := auth.NewSessionStore(rdb, time.Hour)
	reg := auth.NewRegistry(auth.NewEmailPasswordProvider(q, nil))
	if fp != nil {
		if err := reg.Register(fp.name, fp, false); err != nil {
			t.Fatalf("register provider: %v", err)
		}
	}
	limiter := redisx.NewLimiter(rdb)
	h := handlers.New(handlers.Deps{
		Users:        users.New(q, sessions, true),
		Events:       events.New(q, clock.System()),
		Auth:         reg,
		ExternalAuth: auth.NewExternalResolver(q, policy, discardLog()),
		LoginStates:  auth.NewLoginStateStore(rdb, auth.LoginStateTTL),
		BaseURL:      testOrigin,
		Sessions:     sessions,
		Limiter:      limiter,
		Audit:        audit.New(q, discardLog()),
		SessionTTL:   time.Hour,
		Log:          discardLog(),
	})
	return httpserver.New(httpserver.Deps{
		Log: discardLog(), Handlers: h, Sessions: sessions, BaseOrigin: testOrigin,
		V0Sunset: time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC),
		Limiter:  limiter, TokenRateBurst: 6000, TokenRateWindow: time.Minute,
	})
}

func mkLocalUser(t *testing.T, pool *pgxpool.Pool, username, email string) gen.User {
	t.Helper()
	u, err := gen.New(pool).CreateUser(context.Background(), gen.CreateUserParams{
		ID: uuid.Must(uuid.NewV7()), Username: username, Email: email,
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHQ$aGFzaA", Role: "user",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

// stateFromRedirect pulls the state the host put in the authorize URL it redirected to.
func stateFromRedirect(t *testing.T, loc string) string {
	t.Helper()
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect %q: %v", loc, err)
	}
	return u.Query().Get("state")
}

// beginLogin drives GET /auth/{provider}/login and returns the state and the jar holding the
// bound cookie.
func beginLogin(t *testing.T, srv http.Handler, provider string) (string, *cookieJar) {
	t.Helper()
	jar := &cookieJar{}
	rec := do(t, srv, jar, http.MethodGet, "/api/v1/auth/"+provider+"/login", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("begin: status %d, want 302 (body %s)", rec.Code, rec.Body.String())
	}
	jar.update(rec.Result())
	return stateFromRedirect(t, rec.Header().Get("Location")), jar
}

func hasSessionCookie(jar *cookieJar) bool {
	for _, c := range jar.cookies {
		if c.Name == auth.CookieName && c.Value != "" {
			return true
		}
	}
	return false
}

// THE CHAIN: a verified identity matching an existing account binds and produces a working
// session, under the default invite-only policy.
func TestSSOLoginEndToEnd(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	existing := mkLocalUser(t, pool, "ssomember", "member@example.test")
	fp := &fakeRedirect{name: "oidc", identity: auth.ExternalIdentity{
		Subject: "sub-1", Email: existing.Email, EmailVerified: true,
	}}
	srv := ssoServer(t, pool, rdb, auth.ProvisionInviteOnly, fp)

	state, jar := beginLogin(t, srv, "oidc")
	if state == "" {
		t.Fatal("authorize URL carried no state")
	}
	if fp.gotHostState != state {
		t.Fatalf("provider received state %q but the browser was sent %q", fp.gotHostState, state)
	}

	rec := do(t, srv, jar, http.MethodGet, "/api/v1/auth/oidc/callback?state="+url.QueryEscape(state)+"&code=abc", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("callback: status %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Fatalf("callback redirected to %q, want %q (a failure redirect means the login was refused)", loc, "/")
	}
	jar.update(rec.Result())
	if !hasSessionCookie(jar) {
		t.Fatal("no session cookie was set")
	}
	// The provider got its OWN blob back, not the CSRF state.
	if fp.gotProviderBlob != "provider-blob" {
		t.Fatalf("Complete received %q, want the provider's own round-trip blob", fp.gotProviderBlob)
	}

	me := do(t, srv, jar, http.MethodGet, "/api/v1/auth/me", "")
	if me.Code != http.StatusOK {
		t.Fatalf("GET /auth/me after SSO login: status %d", me.Code)
	}
	if !strings.Contains(me.Body.String(), existing.Username) {
		t.Fatalf("session belongs to the wrong account: %s", me.Body.String())
	}
}

// The CSRF guards, each rejected the same way and without a session.
func TestSSOCallbackRejectsBadState(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	u := mkLocalUser(t, pool, "guarded", "guarded@example.test")
	newSrv := func() (http.Handler, *fakeRedirect) {
		fp := &fakeRedirect{name: "oidc", identity: auth.ExternalIdentity{
			Subject: "sub-guard", Email: u.Email, EmailVerified: true,
		}}
		return ssoServer(t, pool, rdb, auth.ProvisionInviteOnly, fp), fp
	}

	t.Run("state does not match the cookie", func(t *testing.T) {
		srv, _ := newSrv()
		_, jar := beginLogin(t, srv, "oidc")
		rec := do(t, srv, jar, http.MethodGet, "/api/v1/auth/oidc/callback?state=someone-elses&code=abc", "")
		assertLoginRefused(t, rec)
	})

	t.Run("no state cookie at all", func(t *testing.T) {
		srv, _ := newSrv()
		state, _ := beginLogin(t, srv, "oidc")
		rec := do(t, srv, &cookieJar{}, http.MethodGet, "/api/v1/auth/oidc/callback?state="+state+"&code=abc", "")
		assertLoginRefused(t, rec)
	})

	t.Run("state is single use", func(t *testing.T) {
		srv, _ := newSrv()
		state, jar := beginLogin(t, srv, "oidc")
		path := "/api/v1/auth/oidc/callback?state=" + url.QueryEscape(state) + "&code=abc"
		if first := do(t, srv, jar, http.MethodGet, path, ""); first.Header().Get("Location") != "/" {
			t.Fatalf("first callback should have succeeded, got %q", first.Header().Get("Location"))
		}
		// A captured callback URL replayed with the same cookie must not log in again.
		assertLoginRefused(t, do(t, srv, jar, http.MethodGet, path, ""))
	})
}

func assertLoginRefused(t *testing.T, rec interface {
	Header() http.Header
	Result() *http.Response
}) {
	t.Helper()
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Fatalf("redirected to %q, want the login page — the login must be refused", loc)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.CookieName && c.Value != "" {
			t.Fatal("a session cookie was set on a refused login")
		}
	}
}

// A provider that substitutes its own state cannot start a login at all. This is the host half of
// the property VerifyAuth checks on the plugin side.
func TestSSOProviderThatIgnoresHostStateCannotStartALogin(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	fp := &fakeRedirect{name: "oidc", ignoreHostState: true}
	srv := ssoServer(t, pool, rdb, auth.ProvisionOpen, fp)

	rec := do(t, srv, &cookieJar{}, http.MethodGet, "/api/v1/auth/oidc/login", "")
	if rec.Code == http.StatusFound {
		t.Fatalf("login started despite the provider substituting its own state (redirect to %q)",
			rec.Header().Get("Location"))
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503", rec.Code)
	}
}

// The provisioning policy, over HTTP, for an identity with no local account.
func TestSSOProvisioningPolicyOverHTTP(t *testing.T) {
	cases := []struct {
		policy     auth.ProvisionPolicy
		wantSignIn bool
	}{
		{auth.ProvisionOpen, true},
		{auth.ProvisionInviteOnly, false},
		{auth.ProvisionOff, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.policy), func(t *testing.T) {
			pool, _ := testsupport.Postgres(t)
			rdb := testsupport.Redis(t)
			fp := &fakeRedirect{name: "oidc", identity: auth.ExternalIdentity{
				Subject:  fmt.Sprintf("newcomer-%s", tc.policy),
				Email:    fmt.Sprintf("newcomer-%s@example.test", tc.policy),
				Username: "Newcomer", EmailVerified: true,
			}}
			srv := ssoServer(t, pool, rdb, tc.policy, fp)

			state, jar := beginLogin(t, srv, "oidc")
			rec := do(t, srv, jar, http.MethodGet,
				"/api/v1/auth/oidc/callback?state="+url.QueryEscape(state)+"&code=abc", "")
			loc := rec.Header().Get("Location")
			if tc.wantSignIn && loc != "/" {
				t.Fatalf("policy %s: redirected to %q, want a successful login", tc.policy, loc)
			}
			if !tc.wantSignIn && loc == "/" {
				t.Fatalf("policy %s: an unknown identity was signed in; only open may provision", tc.policy)
			}
		})
	}
}

// An unverified email must never attach a login to an existing account, over HTTP as in the unit
// tests — this is the account-takeover guard on the real route.
func TestSSOUnverifiedEmailCannotTakeOverAnAccount(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	victim := mkLocalUser(t, pool, "victimuser", "victim@example.test")
	fp := &fakeRedirect{name: "oidc", identity: auth.ExternalIdentity{
		Subject: "attacker", Email: victim.Email, EmailVerified: false,
	}}
	srv := ssoServer(t, pool, rdb, auth.ProvisionOpen, fp)

	state, jar := beginLogin(t, srv, "oidc")
	rec := do(t, srv, jar, http.MethodGet,
		"/api/v1/auth/oidc/callback?state="+url.QueryEscape(state)+"&code=abc", "")
	assertLoginRefused(t, rec)
}

// A broken SSO provider must not take email/password login with it. There is no auth fallback in
// either direction: the built-in keeps working, and it is not consulted for the provider's route.
func TestSSOOutageLeavesEmailLoginWorking(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	fp := &fakeRedirect{name: "oidc", beginErr: errors.New("identity provider unreachable")}
	srv := ssoServer(t, pool, rdb, auth.ProvisionInviteOnly, fp)

	if rec := do(t, srv, &cookieJar{}, http.MethodGet, "/api/v1/auth/oidc/login", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a down provider returned %d, want 503", rec.Code)
	}
	// The built-in path is untouched.
	jar := registerUser(t, srv, "stillworks", "stillworks@example.test")
	if me := do(t, srv, jar, http.MethodGet, "/api/v1/auth/me", ""); me.Code != http.StatusOK {
		t.Fatalf("email login broke while the SSO provider was down: /auth/me returned %d", me.Code)
	}
}

// The provider listing is what a client renders login buttons from.
func TestSSOProviderListing(t *testing.T) {
	pool, _ := testsupport.Postgres(t)
	rdb := testsupport.Redis(t)
	srv := ssoServer(t, pool, rdb, auth.ProvisionInviteOnly, &fakeRedirect{name: "oidc"})

	rec := do(t, srv, &cookieJar{}, http.MethodGet, "/api/v1/auth/providers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"id":"email"`) || !strings.Contains(body, `"redirect":false`) {
		t.Errorf("listing omits the built-in credential provider: %s", body)
	}
	if !strings.Contains(body, `"id":"oidc"`) || !strings.Contains(body, `"redirect":true`) {
		t.Errorf("listing omits the redirect provider: %s", body)
	}
}
