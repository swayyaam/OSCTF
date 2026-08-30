package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/swayyaam/OSCTF/internal/apiclient"
)

func newLoginCmd() *cobra.Command {
	var (
		urlFlag  string
		name     string
		email    string
		password string
		tokenIn  string
		scopes   []string
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate against a deployment and store a token",
		Long: "Stores an API token for a named context.\n\n" +
			"With --token, an existing token is verified and stored. Without it, login is a " +
			"BOOTSTRAP: your email and password mint a token, and the session used to mint it is " +
			"discarded immediately. Minting a token is session-authenticated by design — a token " +
			"cannot mint another token — so this is the one command that handles a password.\n\n" +
			"A deployment with email login disabled (SSO only) cannot be bootstrapped this way: " +
			"a CLI cannot complete a redirect login. Create a token in the dashboard and pass it " +
			"with --token.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := newPrinter(g.json)
			if urlFlag == "" {
				urlFlag = g.url
			}
			if urlFlag == "" {
				return errf(exitUsage, "--url is required (the deployment to log in to)")
			}
			if name == "" {
				name = contextNameFor(urlFlag)
			}

			tok := tokenIn
			if tok == "" {
				var err error
				tok, err = bootstrapToken(cmd, normalizeURL(urlFlag), email, password, scopes)
				if err != nil {
					return err
				}
			}

			// Never store a credential we have not seen work — a context that fails on first use
			// is worse than a login that refused.
			who, err := verifyToken(cmd.Context(), target{url: urlFlag, token: tok})
			if err != nil {
				return err
			}

			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			usedKeychain := storeToken(name, tok)
			entry := Context{URL: normalizeURL(urlFlag), KeychainRef: usedKeychain}
			if !usedKeychain {
				entry.Token = tok
			}
			cfg.Contexts[name] = entry
			cfg.CurrentContext = name
			if err := saveConfig(cfg); err != nil {
				return err
			}

			p.human("Logged in to %s as %s (context %q).", normalizeURL(urlFlag), who.Username, name)
			p.human("%s", keychainNote(usedKeychain))
			return p.data(struct {
				Context  string   `json:"context"`
				URL      string   `json:"url"`
				Username string   `json:"username"`
				Scopes   []string `json:"scopes,omitempty"`
				Keychain bool     `json:"keychain"`
			}{name, normalizeURL(urlFlag), who.Username, who.Scopes, usedKeychain})
		},
	}
	f := cmd.Flags()
	f.StringVar(&urlFlag, "url", "", "deployment URL")
	f.StringVar(&name, "name", "", "context name (default: derived from the URL host)")
	f.StringVar(&email, "email", "", "email for the bootstrap login (prompted when absent)")
	f.StringVar(&password, "password", "", "password for the bootstrap login (prompted when absent; prefer the prompt)")
	f.StringVar(&tokenIn, "token", "", "store this existing token instead of minting one")
	f.StringSliceVar(&scopes, "scope", []string{"read", "submit", "admin"},
		"scopes for the minted token; an admin scope never exceeds your own role")
	return cmd
}

// bootstrapToken exchanges a password for an API token, then throws the session away.
func bootstrapToken(cmd *cobra.Command, baseURL, email, password string, scopes []string) (string, error) {
	p := newPrinter(g.json)
	var err error
	if email == "" {
		email, err = prompt("Email: ")
		if err != nil {
			return "", err
		}
	}
	if password == "" {
		password, err = promptSecret("Password: ")
		if err != nil {
			return "", err
		}
	}

	// A cookie jar the session lives in for the next two calls and nowhere else. It is never
	// written to disk.
	jar, err := cookiejar.New(nil)
	if err != nil {
		return "", errf(exitError, "creating a cookie jar: %v", err)
	}
	httpc := &http.Client{Jar: jar}
	// Session mutations are CSRF-checked against Origin; a bearer request skips that, but this
	// bootstrap is deliberately a session.
	sess, err := apiclient.NewClientWithResponses(baseURL+"/api/v1",
		apiclient.WithHTTPClient(httpc),
		apiclient.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			req.Header.Set("Origin", baseURL)
			return nil
		}))
	if err != nil {
		return "", errf(exitUsage, "building the API client: %v", err)
	}
	ctx := cmd.Context()

	lr, err := sess.LoginWithResponse(ctx, apiclient.LoginJSONRequestBody{
		Email: openapi_types.Email(email), Password: password,
	})
	if err != nil {
		return "", errf(exitUnavailable, "reaching %s: %v", baseURL, err)
	}
	if lr.StatusCode() != http.StatusOK {
		if lr.StatusCode() == http.StatusForbidden {
			// The most likely cause is an SSO-only deployment, and a bare 403 would leave someone
			// guessing. Say what to do instead.
			return "", errf(exitAuth, "this deployment does not accept email/password login "+
				"(it is likely SSO-only). Create a token in the dashboard and run: osctf login --url %s --token …", baseURL)
		}
		return "", apiErr("login", lr.StatusCode(), lr.Body)
	}

	// Discard the session as soon as the token exists, whatever happens next.
	defer func() { _, _ = sess.LogoutWithResponse(ctx) }()

	tr, err := sess.CreateTokenWithResponse(ctx, apiclient.CreateTokenJSONRequestBody{
		Name: fmt.Sprintf("osctf cli (%s)", hostname()), Scopes: toScopes(scopes),
	})
	if err != nil {
		return "", errf(exitUnavailable, "creating a token: %v", err)
	}
	if tr.StatusCode() != http.StatusCreated && tr.StatusCode() != http.StatusOK {
		return "", apiErr("creating a token", tr.StatusCode(), tr.Body)
	}
	if tr.JSON201 == nil {
		return "", errf(exitError, "the server accepted the token request but returned no token")
	}
	p.human("Minted an API token; the login session has been discarded.")
	return tr.JSON201.Token, nil
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the stored credential for a context",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(_ *cobra.Command, _ []string) error {
			p := newPrinter(g.json)
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			name := g.context
			if name == "" {
				name = cfg.CurrentContext
			}
			if name == "" {
				return errf(exitUsage, "no context selected; pass --context")
			}
			if _, ok := cfg.Contexts[name]; !ok {
				return errf(exitNotFound, "no such context %q", name)
			}
			deleteToken(name)
			delete(cfg.Contexts, name)
			if cfg.CurrentContext == name {
				cfg.CurrentContext = ""
			}
			if err := saveConfig(cfg); err != nil {
				return err
			}
			// The token is forgotten locally; it is NOT revoked server-side. Say so, because
			// "logged out" usually implies the credential is dead.
			p.human("Context %q removed. The token still exists on the server — revoke it with "+
				"`osctf token revoke` if it should stop working.", name)
			return p.data(struct {
				Context string `json:"context"`
				Removed bool   `json:"removed"`
			}{name, true})
		},
	}
}

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who the current credential authenticates as",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := newPrinter(g.json)
			t, err := resolveTarget(g.url, g.token, g.context)
			if err != nil {
				return err
			}
			who, err := verifyToken(cmd.Context(), t)
			if err != nil {
				return err
			}
			p.human("%s (%s) at %s — credential from %s", who.Username, who.Role, normalizeURL(t.url), t.from)
			return p.data(who)
		},
	}
}

// identity is what whoami reports.
type identity struct {
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Role     string   `json:"role"`
	Scopes   []string `json:"scopes,omitempty"`
	URL      string   `json:"url"`
}

func verifyToken(ctx context.Context, t target) (identity, error) {
	c, err := newAPIClient(t)
	if err != nil {
		return identity{}, err
	}
	r, err := c.GetMeWithResponse(ctx)
	if err != nil {
		return identity{}, errf(exitUnavailable, "reaching %s: %v", normalizeURL(t.url), err)
	}
	if r.StatusCode() != http.StatusOK {
		return identity{}, apiErr("verifying the credential", r.StatusCode(), r.Body)
	}
	me := r.JSON200
	if me == nil {
		return identity{}, errf(exitError, "the server returned no identity")
	}
	return identity{
		Username: me.Username, Email: string(me.Email), Role: string(me.Role),
		URL: normalizeURL(t.url),
	}, nil
}

func newContextCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "context", Short: "Manage named deployment targets"}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List configured contexts",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(_ *cobra.Command, _ []string) error {
			p := newPrinter(g.json)
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			type row struct {
				Name    string `json:"name"`
				URL     string `json:"url"`
				Current bool   `json:"current"`
			}
			out := make([]row, 0, len(cfg.Contexts))
			for n, c := range cfg.Contexts {
				out = append(out, row{n, c.URL, n == cfg.CurrentContext})
				marker := " "
				if n == cfg.CurrentContext {
					marker = "*"
				}
				p.human("%s %-20s %s", marker, n, c.URL)
			}
			if len(out) == 0 {
				p.human("No contexts configured. Run `osctf login --url …`.")
			}
			return p.data(out)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "use <name>",
		Short: "Select the default context",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(_ *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			if _, ok := cfg.Contexts[args[0]]; !ok {
				return errf(exitNotFound, "no such context %q", args[0])
			}
			cfg.CurrentContext = args[0]
			if err := saveConfig(cfg); err != nil {
				return err
			}
			p.human("Now using context %q.", args[0])
			return p.data(struct {
				Current string `json:"current_context"`
			}{args[0]})
		},
	})
	return cmd
}

func prompt(label string) (string, error) {
	_, _ = fmt.Fprint(os.Stderr, label)
	s := bufio.NewScanner(os.Stdin)
	if !s.Scan() {
		return "", errf(exitUsage, "no input for %q (pass the flag in a non-interactive session)", strings.TrimSpace(label))
	}
	return strings.TrimSpace(s.Text()), nil
}

// promptSecret reads without echoing. If stdin is not a terminal there is nothing safe to do:
// reading a password from a pipe would put it in the caller's history or process list, so the
// flag is the supported path and this says so.
func promptSecret(label string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errf(exitUsage, "cannot prompt for a password without a terminal — "+
			"pass --password, or better, mint a token in the dashboard and use --token")
	}
	_, _ = fmt.Fprint(os.Stderr, label)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", errf(exitError, "reading the password: %v", err)
	}
	return string(b), nil
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown host"
	}
	return h
}

func contextNameFor(rawURL string) string {
	s := strings.TrimPrefix(strings.TrimPrefix(normalizeURL(rawURL), "https://"), "http://")
	if i := strings.IndexAny(s, "/:"); i > 0 {
		s = s[:i]
	}
	if s == "" {
		return "default"
	}
	return s
}

// toScopes converts the flag's strings to the generated scope type. Validation is the server's
// job — sending an unknown scope and getting a 422 with the reason beats a client-side list that
// drifts from the API.
func toScopes(in []string) []apiclient.TokenScope {
	out := make([]apiclient.TokenScope, 0, len(in))
	for _, s := range in {
		out = append(out, apiclient.TokenScope(s))
	}
	return out
}
