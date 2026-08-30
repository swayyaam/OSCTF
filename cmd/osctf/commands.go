package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/swayyaam/OSCTF/internal/apiclient"
	"github.com/swayyaam/OSCTF/internal/challengespec"
)

// call runs one API request and turns a non-2xx into the taxonomy's error. Every remote command
// funnels through it so status→exit-code mapping lives in exactly one place.
func call[T any](what string, do func(c *apiclient.ClientWithResponses) (*T, error),
	status func(*T) int, body func(*T) []byte) (*T, error) {
	c, t, err := apiFor()
	if err != nil {
		return nil, err
	}
	r, err := do(c)
	if err != nil {
		return nil, errf(exitUnavailable, "reaching %s: %v", normalizeURL(t.url), err)
	}
	if s := status(r); s < 200 || s >= 300 {
		return nil, apiErr(what, s, body(r))
	}
	return r, nil
}

// ---------------------------------------------------------------------------- challenge

func newChallengeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "challenge", Short: "Author and manage challenges"}

	validate := &cobra.Command{
		Use:   "validate <dir>",
		Short: "Validate a challenge directory offline",
		Long: "Parses and validates challenge.yaml using the SAME rules the server applies on " +
			"create, so a verdict here is the verdict there. Needs no network and no token.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(_ *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			dir := filepath.Clean(args[0])
			spec, err := challengespec.ParseFile(filepath.Join(dir, "challenge.yaml"), filepath.Base(dir))
			if err != nil {
				var ve *challengespec.ValidationError
				if asValidation(err, &ve) {
					p.human("invalid: %s", ve.Message)
					_ = p.data(struct {
						OK          bool                `json:"ok"`
						FieldErrors map[string][]string `json:"field_errors"`
					}{false, ve.FieldErrors()})
					return &cliError{code: exitValidation, msg: ve.Message, fields: ve.FieldErrors(), reported: true}
				}
				return errf(exitValidation, "%v", err)
			}
			p.human("%s is valid (%s, %s).", dir, spec.Kind, spec.Scoring)
			return p.data(struct {
				OK   bool   `json:"ok"`
				Slug string `json:"slug"`
				Kind string `json:"kind"`
			}{true, spec.Slug, spec.Kind})
		},
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List challenges",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := newPrinter(g.json)
			r, err := call("listing challenges",
				func(c *apiclient.ClientWithResponses) (*apiclient.ListChallengesResponse, error) {
					return c.ListChallengesWithResponse(cmd.Context())
				},
				func(r *apiclient.ListChallengesResponse) int { return r.StatusCode() },
				func(r *apiclient.ListChallengesResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			if r.JSON200 != nil {
				for _, c := range *r.JSON200 {
					p.human("%-28s %-12s %4d", c.Slug, c.Category, c.Points)
				}
			}
			return p.data(r.JSON200)
		},
	}

	get := &cobra.Command{
		Use:   "get <slug>",
		Short: "Show one challenge",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			r, err := call("getting the challenge",
				func(c *apiclient.ClientWithResponses) (*apiclient.GetChallengeResponse, error) {
					return c.GetChallengeWithResponse(cmd.Context(), args[0])
				},
				func(r *apiclient.GetChallengeResponse) int { return r.StatusCode() },
				func(r *apiclient.GetChallengeResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			if r.JSON200 != nil {
				p.human("%s — %s (%d points)", r.JSON200.Slug, r.JSON200.Title, r.JSON200.Points)
			}
			return p.data(r.JSON200)
		},
	}

	cmd.AddCommand(validate, list, get)
	return cmd
}

// asValidation is errors.As specialised, kept here so the command reads cleanly.
func asValidation(err error, target **challengespec.ValidationError) bool {
	for e := err; e != nil; {
		if ve, ok := e.(*challengespec.ValidationError); ok {
			*target = ve
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// ---------------------------------------------------------------------------- event

func newEventCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "event", Short: "Read or set the event window"}

	cmd.AddCommand(&cobra.Command{
		Use:   "get",
		Short: "Show the event window and phase",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := newPrinter(g.json)
			r, err := call("getting the event",
				func(c *apiclient.ClientWithResponses) (*apiclient.GetEventResponse, error) {
					return c.GetEventWithResponse(cmd.Context())
				},
				func(r *apiclient.GetEventResponse) int { return r.StatusCode() },
				func(r *apiclient.GetEventResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			if r.JSON200 != nil {
				p.human("%s — phase %s", r.JSON200.Name, r.JSON200.Phase)
			}
			return p.data(r.JSON200)
		},
	})

	var start, end, freeze string
	set := &cobra.Command{
		Use:   "set",
		Short: "Set the event window (admin)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := newPrinter(g.json)
			body := apiclient.AdminUpdateEventJSONRequestBody{}
			if start != "" {
				ts, err := parseTime("--start", start)
				if err != nil {
					return err
				}
				body.StartsAt = &ts
			}
			if end != "" {
				ts, err := parseTime("--end", end)
				if err != nil {
					return err
				}
				body.EndsAt = &ts
			}
			if freeze != "" {
				ts, err := parseTime("--freeze", freeze)
				if err != nil {
					return err
				}
				body.FreezeAt = &ts
			}
			if body.StartsAt == nil && body.EndsAt == nil && body.FreezeAt == nil {
				return errf(exitUsage, "nothing to set: pass --start, --end, or --freeze")
			}
			r, err := call("updating the event",
				func(c *apiclient.ClientWithResponses) (*apiclient.AdminUpdateEventResponse, error) {
					return c.AdminUpdateEventWithResponse(cmd.Context(), body)
				},
				func(r *apiclient.AdminUpdateEventResponse) int { return r.StatusCode() },
				func(r *apiclient.AdminUpdateEventResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			p.human("Event updated.")
			return p.data(r.JSON200)
		},
	}
	set.Flags().StringVar(&start, "start", "", "window start (RFC 3339)")
	set.Flags().StringVar(&end, "end", "", "window end (RFC 3339)")
	set.Flags().StringVar(&freeze, "freeze", "", "scoreboard freeze time (RFC 3339)")
	cmd.AddCommand(set)
	return cmd
}

func parseTime(flag, v string) (time.Time, error) {
	ts, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, errf(exitUsage, "%s must be RFC 3339 (e.g. 2026-09-01T00:00:00Z), got %q", flag, v)
	}
	return ts, nil
}

// ---------------------------------------------------------------------------- scoreboard

func newScoreboardCmd() *cobra.Command {
	var top int
	cmd := &cobra.Command{
		Use:   "scoreboard",
		Short: "Show the standings",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := newPrinter(g.json)
			r, err := call("reading the scoreboard",
				func(c *apiclient.ClientWithResponses) (*apiclient.GetScoreboardResponse, error) {
					return c.GetScoreboardWithResponse(cmd.Context())
				},
				func(r *apiclient.GetScoreboardResponse) int { return r.StatusCode() },
				func(r *apiclient.GetScoreboardResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			sb := r.JSON200
			if sb == nil {
				return p.data(nil)
			}
			rows := sb.Standings
			if top > 0 && len(rows) > top {
				rows = rows[:top]
			}
			if sb.Frozen {
				p.human("(frozen)")
			}
			for i, s := range rows {
				p.human("%3d. %-24s %5d", i+1, s.Name, s.Points)
			}
			return p.data(struct {
				Frozen    bool `json:"frozen"`
				Standings any  `json:"standings"`
			}{sb.Frozen, rows})
		},
	}
	cmd.Flags().IntVar(&top, "top", 0, "show only the first N teams")
	return cmd
}

// ---------------------------------------------------------------------------- submit

func newSubmitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "submit <slug> <flag>",
		Short: "Submit a flag",
		Long: "Submits a flag for your team. Exit 0 whatever the verdict — a wrong flag is a " +
			"successful request, and `correct` in the output is the answer. Exit 5 means the " +
			"challenge was already solved, 3 that attempts are exhausted or the team is banned.",
		Args: usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			r, err := call("submitting the flag",
				func(c *apiclient.ClientWithResponses) (*apiclient.SubmitFlagResponse, error) {
					return c.SubmitFlagWithResponse(cmd.Context(), args[0],
						apiclient.SubmitFlagJSONRequestBody{Flag: args[1]})
				},
				func(r *apiclient.SubmitFlagResponse) int { return r.StatusCode() },
				func(r *apiclient.SubmitFlagResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			if r.JSON200 != nil {
				if r.JSON200.Correct {
					p.human("correct")
				} else {
					p.human("incorrect")
				}
			}
			return p.data(r.JSON200)
		},
	}
}

// ---------------------------------------------------------------------------- instance

func newInstanceCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "instance", Short: "Per-team challenge instances"}

	cmd.AddCommand(&cobra.Command{
		Use:   "start <slug>",
		Short: "Start your team's instance",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			r, err := call("starting the instance",
				func(c *apiclient.ClientWithResponses) (*apiclient.StartInstanceResponse, error) {
					return c.StartInstanceWithResponse(cmd.Context(), args[0])
				},
				func(r *apiclient.StartInstanceResponse) int { return r.StatusCode() },
				func(r *apiclient.StartInstanceResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			return p.data(r.JSON200)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "stop <slug>",
		Short: "Stop your team's instance",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			_, err := call("stopping the instance",
				func(c *apiclient.ClientWithResponses) (*apiclient.StopInstanceResponse, error) {
					return c.StopInstanceWithResponse(cmd.Context(), args[0])
				},
				func(r *apiclient.StopInstanceResponse) int { return r.StatusCode() },
				func(r *apiclient.StopInstanceResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			p.human("Instance stopped.")
			return p.data(struct {
				Stopped bool `json:"stopped"`
			}{true})
		},
	})
	return cmd
}

// ---------------------------------------------------------------------------- token

func newTokenCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "token", Short: "Manage your API tokens"}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List your tokens (metadata only)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := newPrinter(g.json)
			r, err := call("listing tokens",
				func(c *apiclient.ClientWithResponses) (*apiclient.ListTokensResponse, error) {
					return c.ListTokensWithResponse(cmd.Context())
				},
				func(r *apiclient.ListTokensResponse) int { return r.StatusCode() },
				func(r *apiclient.ListTokensResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			if r.JSON200 != nil {
				for _, t := range *r.JSON200 {
					p.human("%s  %-20s %v", t.Prefix, t.Name, t.Scopes)
				}
			}
			return p.data(r.JSON200)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "revoke <id>",
		Short: "Revoke a token immediately",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			id, err := parseUUID(args[0])
			if err != nil {
				return err
			}
			if !g.yes && !confirm(fmt.Sprintf("Revoke token %s? Anything using it stops working immediately.", args[0])) {
				return errf(exitUsage, "aborted")
			}
			_, err = call("revoking the token",
				func(c *apiclient.ClientWithResponses) (*apiclient.RevokeTokenResponse, error) {
					return c.RevokeTokenWithResponse(cmd.Context(), id)
				},
				func(r *apiclient.RevokeTokenResponse) int { return r.StatusCode() },
				func(r *apiclient.RevokeTokenResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			p.human("Token revoked.")
			return p.data(struct {
				Revoked string `json:"revoked"`
			}{args[0]})
		},
	})
	return cmd
}

// ---------------------------------------------------------------------------- plugin

func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "plugin", Short: "Inspect and reload plugins (admin)"}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Show every tracked plugin and its state",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := newPrinter(g.json)
			r, err := call("listing plugins",
				func(c *apiclient.ClientWithResponses) (*apiclient.AdminListPluginsResponse, error) {
					return c.AdminListPluginsWithResponse(cmd.Context())
				},
				func(r *apiclient.AdminListPluginsResponse) int { return r.StatusCode() },
				func(r *apiclient.AdminListPluginsResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			if r.JSON200 != nil {
				for _, pl := range r.JSON200.Plugins {
					reason := ""
					if pl.Reason != nil {
						reason = "  " + *pl.Reason
					}
					p.human("%-20s %-16s %s%s", pl.Name, pl.Type, pl.State, reason)
				}
			}
			return p.data(r.JSON200)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "reload <name>",
		Short: "Hot-reload one plugin",
		Long: "Launches a new instance and swaps to it once ready. If the new one never becomes " +
			"ready the OLD one keeps serving and this reports 7 — a failed reload leaves the " +
			"deployment unchanged, never without the plugin.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := newPrinter(g.json)
			_, err := call("reloading the plugin",
				func(c *apiclient.ClientWithResponses) (*apiclient.AdminReloadPluginResponse, error) {
					return c.AdminReloadPluginWithResponse(cmd.Context(), args[0])
				},
				func(r *apiclient.AdminReloadPluginResponse) int { return r.StatusCode() },
				func(r *apiclient.AdminReloadPluginResponse) []byte { return r.Body })
			if err != nil {
				return err
			}
			p.human("Reloaded %s; the new instance is serving.", args[0])
			return p.data(struct {
				Reloaded string `json:"reloaded"`
			}{args[0]})
		},
	})
	return cmd
}

// confirm asks before something hard to reverse. --yes skips it, which is what CI and agents use.
func confirm(question string) bool {
	_, _ = fmt.Fprintf(os.Stderr, "%s [y/N] ", question)
	ans, err := prompt("")
	if err != nil {
		return false
	}
	return ans == "y" || ans == "Y" || ans == "yes"
}

func parseUUID(s string) (apiclient.IdPath, error) {
	var id apiclient.IdPath
	b, err := json.Marshal(s)
	if err != nil {
		return id, errf(exitUsage, "invalid id %q", s)
	}
	if err := json.Unmarshal(b, &id); err != nil {
		return id, errf(exitUsage, "invalid id %q: expected a UUID", s)
	}
	return id, nil
}

var _ = http.StatusOK
