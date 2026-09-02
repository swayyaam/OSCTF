package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
)

// The MCP server: a thin Model Context Protocol adapter over the API-v1 client, so an agent can
// operate a deployment conversationally. Every tool is one API call under the caller's own token.
//
// Three properties do the safety work, and each is enforced rather than documented:
//
//  1. SCOPE GATES EXPOSURE. The server asks the API what its own token may do and registers only
//     those tools, so an agent with a read token cannot see admin tools — it is not told "no", it
//     is never offered the option. Guessing here would be worse than not gating: an advertised
//     tool that always fails teaches an agent to retry.
//  2. DESTRUCTIVE TOOLS REQUIRE confirm:true, and say what they would affect when refused. This
//     mirrors the CLI's --yes and keeps agent operation reversible by default.
//  3. THERE IS NO PRIVILEGED PATH. No shell, no filesystem, no database. Anything this can do, a
//     human with the same token can do in the dashboard, and the server inherits every check the
//     API applies.

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Run the MCP server over stdio",
		Long: "Serves the platform as Model Context Protocol tools on stdio, for an agent to " +
			"operate.\n\nRegister it with any MCP client as `command: osctf, args: [mcp]`. " +
			"Authentication is the same API token every other command uses, and the token's scope " +
			"decides which tools exist at all.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := resolveTarget(g.url, g.token, g.context)
			if err != nil {
				return err
			}
			if t.token == "" {
				return errf(exitAuth, "the MCP server needs an API token: set OSCTF_TOKEN or run `osctf login`")
			}
			api, err := newAPIClient(t)
			if err != nil {
				return err
			}

			// Ask the API what this token may do. Doing it once at start-up means the tool list is
			// honest for the whole session, and a dead or unscoped token fails HERE, where the
			// operator sees it, rather than on the agent's first call.
			who, err := verifyToken(cmd.Context(), t)
			if err != nil {
				return err
			}
			granted := scopeSet(who.Scopes)

			srv := mcp.NewServer(
				&mcp.Implementation{Name: "osctf", Version: version},
				&mcp.ServerOptions{Instructions: instructionsFor(who, granted)},
			)
			registerTools(srv, api, granted)
			// stdio is the only transport: an MCP client launches this process and speaks over the
			// pipe, so there is no port to expose and no second authentication path to get wrong.
			return srv.Run(cmd.Context(), &mcp.StdioTransport{})
		},
	}
}

type scopes map[string]bool

func scopeSet(in []string) scopes {
	s := scopes{}
	for _, v := range in {
		s[v] = true
	}
	return s
}

func (s scopes) has(scope string) bool { return s[scope] }

func instructionsFor(who identity, granted scopes) string {
	list := make([]string, 0, len(granted))
	for k := range granted {
		list = append(list, k)
	}
	if len(list) == 0 {
		list = append(list, "none")
	}
	return fmt.Sprintf(
		"OSCTF at %s. You are acting as %s (role %s) with token scopes: %s.\n\n"+
			"Only tools this token's scope permits are registered, so the tool list IS the list of "+
			"things you can do — if an operation is not offered, this credential cannot perform it "+
			"and retrying will not help. Ask the operator for a token with wider scope instead.\n\n"+
			"Tools that change or destroy state require confirm:true. Call once without it to see "+
			"exactly what would be affected, then again with it if that is what you intend.",
		who.URL, who.Username, who.Role, strings.Join(list, ", "))
}

// ------------------------------------------------------------------ tool plumbing

type noArgs struct{}

// confirmArgs is embedded by every tool that changes state.
type confirmArgs struct {
	Confirm bool `json:"confirm,omitempty" jsonschema:"set true to actually perform this; without it the tool reports what it would affect and does nothing"`
}

func textResult(v any) (*mcp.CallToolResult, any, error) {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolErr("encoding the result: %v", err)
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil, nil
}

// toolErr returns a tool-level error. It is deliberately IsError content rather than a Go error:
// the agent should see what went wrong and correct itself, which a transport-level failure does
// not allow.
func toolErr(format string, a ...any) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, a...)}},
	}, nil, nil
}

// needsConfirm is the refusal a destructive tool gives when confirm was not set. It names what
// would happen, so the agent's next call is an informed one rather than a retry.
func needsConfirm(what string) (*mcp.CallToolResult, any, error) {
	return toolErr("Refused: this would %s. Nothing has changed. Call again with confirm:true if "+
		"that is what you intend.", what)
}

// apiFail turns a non-2xx into tool-visible text, passing the server's own problem+json through —
// a 422 with field errors is how an agent fixes its own input without guessing.
func apiFail(what string, status int, body []byte) (*mcp.CallToolResult, any, error) {
	var p struct {
		Title  string              `json:"title"`
		Detail string              `json:"detail"`
		Errors map[string][]string `json:"errors"`
	}
	msg := fmt.Sprintf("%s failed (HTTP %d)", what, status)
	if json.Unmarshal(body, &p) == nil && p.Title != "" {
		msg = fmt.Sprintf("%s: %s", what, p.Title)
		if p.Detail != "" {
			msg += " — " + p.Detail
		}
		if len(p.Errors) > 0 {
			f, _ := json.Marshal(p.Errors)
			msg += "\nfield errors: " + string(f)
		}
	}
	if status == 401 || status == 403 {
		msg += "\n(This token does not permit that. A wider scope is needed; retrying will not help.)"
	}
	return toolErr("%s", msg)
}

func readOnly() *mcp.ToolAnnotations {
	t := true
	return &mcp.ToolAnnotations{ReadOnlyHint: t}
}
