package main

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Scope gating is the MCP server's central safety property: an agent with a read token must not
// merely be REFUSED admin operations, it must never be offered them. An advertised tool that
// always fails teaches an agent to retry; a tool that does not exist teaches it to ask for a
// better credential.
//
// This drives the real protocol — a client connected to the server over an in-memory transport,
// calling tools/list — rather than inspecting the table, because what matters is what a client
// actually sees.
func listToolsWithScopes(t *testing.T, granted scopes) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	srv := mcp.NewServer(&mcp.Implementation{Name: "osctf-test", Version: "test"},
		&mcp.ServerOptions{Instructions: "test"})
	// A nil API client is safe here: registration must not call the platform, and if it ever
	// starts to, this panics rather than silently passing.
	registerTools(srv, nil, granted)

	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "probe", Version: "test"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	names := make([]string, 0, len(res.Tools))
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	return names
}

func TestScopeGatesTheToolSurface(t *testing.T) {
	t.Run("a read token sees no write or admin tools", func(t *testing.T) {
		got := listToolsWithScopes(t, scopeSet([]string{"read"}))
		for _, forbidden := range []string{"submit_flag", "start_instance", "stop_instance",
			"set_event", "reload_plugin", "list_plugins", "list_users"} {
			if contains(got, forbidden) {
				t.Errorf("a read-only token was offered %q", forbidden)
			}
		}
		for _, want := range []string{"whoami", "list_challenges", "get_scoreboard"} {
			if !contains(got, want) {
				t.Errorf("a read token was NOT offered %q", want)
			}
		}
	})

	t.Run("submit adds write tools but no admin tools", func(t *testing.T) {
		got := listToolsWithScopes(t, scopeSet([]string{"read", "submit"}))
		if !contains(got, "submit_flag") {
			t.Error("submit scope did not expose submit_flag")
		}
		for _, forbidden := range []string{"set_event", "reload_plugin", "list_users"} {
			if contains(got, forbidden) {
				t.Errorf("a non-admin token was offered %q", forbidden)
			}
		}
	})

	t.Run("admin sees everything", func(t *testing.T) {
		got := listToolsWithScopes(t, scopeSet([]string{"read", "submit", "admin"}))
		for _, want := range toolNamesFor(scopeSet([]string{"read", "submit", "admin"})) {
			if !contains(got, want) {
				t.Errorf("an admin token was not offered %q", want)
			}
		}
	})

	t.Run("no scopes means no tools at all", func(t *testing.T) {
		// A token with no usable scope should present an empty surface rather than a menu of
		// operations that all fail.
		if got := listToolsWithScopes(t, scopeSet(nil)); len(got) != 0 {
			t.Errorf("a scopeless token was offered %v", got)
		}
	})
}

// The registration table and toolNamesFor must agree, since the latter is what the tests and the
// instructions reason about. If they drift, a test could pass while the server exposes something
// else entirely.
func TestToolTableMatchesRegistration(t *testing.T) {
	for _, set := range [][]string{{"read"}, {"read", "submit"}, {"read", "submit", "admin"}, {"admin"}} {
		granted := scopeSet(set)
		want := toolNamesFor(granted)
		sort.Strings(want)
		got := listToolsWithScopes(t, granted)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("scopes %v: registered %v, table says %v", set, got, want)
		}
	}
}

// A destructive tool must refuse without confirm:true, and say what it would have done — a bare
// refusal gives an agent nothing to decide with.
func TestDestructiveToolsRefuseWithoutConfirm(t *testing.T) {
	res, _, err := needsConfirm("destroy your team's running instance of web-login")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Fatal("a refusal was not marked as an error, so an agent would read it as success")
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "Nothing has changed") {
		t.Errorf("the refusal does not say the state is unchanged: %q", text)
	}
	if !strings.Contains(text, "web-login") {
		t.Errorf("the refusal does not say WHAT would be affected: %q", text)
	}
	if !strings.Contains(text, "confirm:true") {
		t.Errorf("the refusal does not say how to proceed: %q", text)
	}
}

// A 401/403 must tell the agent that retrying is pointless. Without that, the natural behaviour
// is to try again, and again.
func TestAuthFailureTellsTheAgentNotToRetry(t *testing.T) {
	res, _, err := apiFail("set_event", 403, []byte(`{"title":"Forbidden"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "retrying will not help") {
		t.Errorf("a 403 does not tell the agent to stop retrying: %q", text)
	}
}

// A 422's field errors must reach the agent, since that is how it corrects its own input.
func TestValidationErrorsReachTheAgent(t *testing.T) {
	res, _, err := apiFail("set_event", 422,
		[]byte(`{"title":"Validation failed","errors":{"starts_at":["must be before ends_at"]}}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, "starts_at") || !strings.Contains(text, "must be before ends_at") {
		t.Errorf("field errors did not reach the agent: %q", text)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
