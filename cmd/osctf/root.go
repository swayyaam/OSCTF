package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/swayyaam/OSCTF/internal/apiclient"
)

// globals are the flags every command shares.
type globals struct {
	url     string
	token   string
	context string
	json    bool
	yes     bool
}

var g globals

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "osctf",
		Short: "Operate an OSCTF deployment from the terminal or CI",
		Long: "osctf is a client for the OSCTF API v1.\n\n" +
			"It holds no business logic and never touches a database: everything it does, it does " +
			"through the same endpoints the dashboard uses, with the same authorization. If a command " +
			"seems to need something the API cannot do, that is a missing endpoint, not a missing flag.\n\n" +
			"Built to be scripted: --json on every command, a flag for every prompt, and exit codes " +
			"mapped to HTTP classes (0 ok, 2 usage, 3 auth, 4 not found, 5 conflict, 6 invalid, 7 unavailable).",
		SilenceUsage:  true, // usage on every runtime error buries the message
		SilenceErrors: true, // main renders errors, so JSON mode stays parseable
	}
	// A bad flag is a USAGE error (exit 2), not a generic failure. Cobra returns a plain error
	// for these, which would land on exit 1 and tell a script the wrong thing.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &cliError{code: exitUsage, msg: err.Error()}
	})

	pf := root.PersistentFlags()
	pf.StringVar(&g.url, "url", "", "deployment URL (overrides OSCTF_URL and the context)")
	pf.StringVar(&g.token, "token", "", "API token (overrides OSCTF_TOKEN and the context)")
	pf.StringVar(&g.context, "context", "", "named context to use (overrides OSCTF_CONTEXT)")
	pf.BoolVar(&g.json, "json", false, "emit a single JSON value on stdout; prose goes to stderr")
	pf.BoolVar(&g.yes, "yes", false, "skip confirmation prompts (for CI and agents)")

	root.AddCommand(
		newVersionCmd(),
		newLoginCmd(),
		newLogoutCmd(),
		newWhoamiCmd(),
		newContextCmd(),
		newInitCmd(),
		newChallengeCmd(),
		newEventCmd(),
		newScoreboardCmd(),
		newSubmitCmd(),
		newInstanceCmd(),
		newTeamCmd(),
		newUserCmd(),
		newTokenCmd(),
		newPluginCmd(),
		newMCPCmd(),
	)
	return root
}

// usageArgs re-codes cobra's positional-argument errors as usage errors, for the same reason as
// SetFlagErrorFunc above: "wrong number of arguments" is exit 2, and every command's Args goes
// through here so none of them can quietly disagree.
func usageArgs(fn cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, a []string) error {
		if err := fn(cmd, a); err != nil {
			return &cliError{code: exitUsage, msg: err.Error()}
		}
		return nil
	}
}

// apiFor builds an authenticated client for the resolved target.
func apiFor() (*apiclient.ClientWithResponses, target, error) {
	t, err := resolveTarget(g.url, g.token, g.context)
	if err != nil {
		return nil, target{}, err
	}
	if t.token == "" {
		return nil, target{}, errf(exitAuth, "no API token: pass --token, set OSCTF_TOKEN, or run `osctf login`")
	}
	c, err := newAPIClient(t)
	return c, t, err
}

func newAPIClient(t target) (*apiclient.ClientWithResponses, error) {
	// The client targets /api/v1 — the stable surface. /api/v0 is a deprecated alias and no
	// client of ours should be reaching for it.
	c, err := apiclient.NewClientWithResponses(normalizeURL(t.url)+"/api/v1",
		apiclient.WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
			if t.token != "" {
				req.Header.Set("Authorization", "Bearer "+t.token)
			}
			return nil
		}))
	if err != nil {
		return nil, errf(exitUsage, "building the API client for %q: %v", t.url, err)
	}
	return c, nil
}

// apiErr turns a non-2xx response into a cliError carrying the taxonomy's exit code and, where
// the server sent problem+json, its detail and field errors. Surfacing the server's own words
// matters: a 422 with field_errors is how an agent corrects itself without guessing.
func apiErr(what string, status int, body []byte) error {
	e := &cliError{code: exitForStatus(status), msg: fmt.Sprintf("%s failed (HTTP %d)", what, status)}
	var p struct {
		Title  string              `json:"title"`
		Detail string              `json:"detail"`
		Errors map[string][]string `json:"errors"`
	}
	if json.Unmarshal(body, &p) == nil {
		if p.Title != "" {
			e.msg = fmt.Sprintf("%s: %s", what, p.Title)
		}
		e.detail = p.Detail
		e.fields = p.Errors
	}
	return e
}
