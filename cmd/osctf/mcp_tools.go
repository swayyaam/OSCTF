package main

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/swayyaam/OSCTF/internal/apiclient"
)

// registerTools registers exactly the tools the token's scope permits.
//
// The gating is the security property, so it is expressed once, here, as a table: a tool's scope
// sits next to its registration and cannot drift from it. Scattering `if scopes.has("admin")`
// through thirteen handlers is how one eventually gets forgotten.
func registerTools(s *mcp.Server, api *apiclient.ClientWithResponses, granted scopes) {
	read := granted.has("read")
	submit := granted.has("submit")
	admin := granted.has("admin")

	// --- read -------------------------------------------------------------------------------
	if read {
		mcp.AddTool(s, &mcp.Tool{
			Name:        "whoami",
			Description: "Who this credential authenticates as, and what its scopes are.",
			Annotations: readOnly(),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			r, err := api.GetMeWithResponse(ctx)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() != 200 {
				return apiFail("whoami", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "list_challenges",
			Description: "Every challenge visible to this account, with slug, category and current points.",
			Annotations: readOnly(),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			r, err := api.ListChallengesWithResponse(ctx)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() != 200 {
				return apiFail("list_challenges", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "get_challenge",
			Description: "Full detail for one challenge, including connection info when an instance is running.",
			Annotations: readOnly(),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Slug string `json:"slug" jsonschema:"the challenge slug, as listed by list_challenges"`
		}) (*mcp.CallToolResult, any, error) {
			r, err := api.GetChallengeWithResponse(ctx, in.Slug)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() != 200 {
				return apiFail("get_challenge", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "get_scoreboard",
			Description: "The standings. Reports whether the board is frozen; a frozen board is intentionally stale.",
			Annotations: readOnly(),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			r, err := api.GetScoreboardWithResponse(ctx)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() != 200 {
				return apiFail("get_scoreboard", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "get_event",
			Description: "The event window and current phase (before, running, frozen, ended).",
			Annotations: readOnly(),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			r, err := api.GetEventWithResponse(ctx)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() != 200 {
				return apiFail("get_event", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "list_teams",
			Description: "Teams in this event.",
			Annotations: readOnly(),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			r, err := api.ListTeamsWithResponse(ctx)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() != 200 {
				return apiFail("list_teams", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})
	}

	// --- submit -----------------------------------------------------------------------------
	if submit {
		mcp.AddTool(s, &mcp.Tool{
			Name: "submit_flag",
			Description: "Submit a flag for your team. Returns the verdict. A wrong flag is a normal " +
				"result, not an error — and it still consumes an attempt where the challenge caps them.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Slug string `json:"slug" jsonschema:"the challenge slug"`
			Flag string `json:"flag" jsonschema:"the flag text to submit"`
		}) (*mcp.CallToolResult, any, error) {
			r, err := api.SubmitFlagWithResponse(ctx, in.Slug, apiclient.SubmitFlagJSONRequestBody{Flag: in.Flag})
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() != 200 {
				return apiFail("submit_flag", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "start_instance",
			Description: "Start your team's own container instance for a per-team challenge.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Slug string `json:"slug" jsonschema:"the challenge slug"`
		}) (*mcp.CallToolResult, any, error) {
			r, err := api.StartInstanceWithResponse(ctx, in.Slug)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() < 200 || r.StatusCode() >= 300 {
				return apiFail("start_instance", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name: "stop_instance",
			Description: "Stop your team's instance. Any unsaved state inside the container is lost, " +
				"and a restart gets a fresh container. Requires confirm:true.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Slug string `json:"slug" jsonschema:"the challenge slug"`
			confirmArgs
		}) (*mcp.CallToolResult, any, error) {
			if !in.Confirm {
				return needsConfirm("destroy your team's running instance of " + in.Slug +
					", losing anything unsaved inside it")
			}
			r, err := api.StopInstanceWithResponse(ctx, in.Slug)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() < 200 || r.StatusCode() >= 300 {
				return apiFail("stop_instance", r.StatusCode(), r.Body)
			}
			return textResult(map[string]any{"stopped": in.Slug})
		})
	}

	// --- admin ------------------------------------------------------------------------------
	if admin {
		mcp.AddTool(s, &mcp.Tool{
			Name: "set_event",
			Description: "Set the event window or freeze time (RFC 3339). Changing a running event's " +
				"window affects every participant immediately. Requires confirm:true.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			StartsAt string `json:"starts_at,omitempty" jsonschema:"window start, RFC 3339"`
			EndsAt   string `json:"ends_at,omitempty" jsonschema:"window end, RFC 3339"`
			FreezeAt string `json:"freeze_at,omitempty" jsonschema:"scoreboard freeze time, RFC 3339"`
			confirmArgs
		}) (*mcp.CallToolResult, any, error) {
			body := apiclient.AdminUpdateEventJSONRequestBody{}
			if in.StartsAt != "" {
				ts, err := parseTime("starts_at", in.StartsAt)
				if err != nil {
					return toolErr("%v", err)
				}
				body.StartsAt = &ts
			}
			if in.EndsAt != "" {
				ts, err := parseTime("ends_at", in.EndsAt)
				if err != nil {
					return toolErr("%v", err)
				}
				body.EndsAt = &ts
			}
			if in.FreezeAt != "" {
				ts, err := parseTime("freeze_at", in.FreezeAt)
				if err != nil {
					return toolErr("%v", err)
				}
				body.FreezeAt = &ts
			}
			if body.StartsAt == nil && body.EndsAt == nil && body.FreezeAt == nil {
				return toolErr("nothing to set: give starts_at, ends_at, or freeze_at")
			}
			if !in.Confirm {
				return needsConfirm("change the event window for every participant")
			}
			r, err := api.AdminUpdateEventWithResponse(ctx, body)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() != 200 {
				return apiFail("set_event", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "list_plugins",
			Description: "Every tracked plugin and its state, including ones quarantined at load.",
			Annotations: readOnly(),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			r, err := api.AdminListPluginsWithResponse(ctx)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() != 200 {
				return apiFail("list_plugins", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})

		mcp.AddTool(s, &mcp.Tool{
			Name: "reload_plugin",
			Description: "Hot-reload one plugin. If the new instance never becomes ready the OLD one " +
				"keeps serving, so a failed reload leaves the deployment unchanged. Requires confirm:true.",
		}, func(ctx context.Context, _ *mcp.CallToolRequest, in struct {
			Name string `json:"name" jsonschema:"the plugin's manifest name, as listed by list_plugins"`
			confirmArgs
		}) (*mcp.CallToolResult, any, error) {
			if !in.Confirm {
				return needsConfirm("relaunch the " + in.Name + " plugin process, briefly interrupting " +
					"anything it serves")
			}
			r, err := api.AdminReloadPluginWithResponse(ctx, in.Name)
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() < 200 || r.StatusCode() >= 300 {
				return apiFail("reload_plugin", r.StatusCode(), r.Body)
			}
			return textResult(map[string]any{"reloaded": in.Name})
		})

		mcp.AddTool(s, &mcp.Tool{
			Name:        "list_users",
			Description: "Accounts in this deployment.",
			Annotations: readOnly(),
		}, func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			r, err := api.AdminListUsersWithResponse(ctx, &apiclient.AdminListUsersParams{})
			if err != nil {
				return toolErr("reaching the platform: %v", err)
			}
			if r.StatusCode() != 200 {
				return apiFail("list_users", r.StatusCode(), r.Body)
			}
			return textResult(r.JSON200)
		})
	}
}

// toolNamesFor reports which tools a scope set would expose. It exists so the gating can be
// tested without standing up a server, and so the table above has exactly one reader.
func toolNamesFor(granted scopes) []string {
	var out []string
	if granted.has("read") {
		out = append(out, "whoami", "list_challenges", "get_challenge", "get_scoreboard", "get_event", "list_teams")
	}
	if granted.has("submit") {
		out = append(out, "submit_flag", "start_instance", "stop_instance")
	}
	if granted.has("admin") {
		out = append(out, "set_event", "list_plugins", "reload_plugin", "list_users")
	}
	return out
}
