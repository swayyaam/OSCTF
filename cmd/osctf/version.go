package main

import (
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is set by the release build via -ldflags; "dev" for a local build.
var version = "dev"

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the client version",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			rev := ""
			if bi, ok := debug.ReadBuildInfo(); ok {
				for _, s := range bi.Settings {
					if s.Key == "vcs.revision" {
						rev = s.Value
					}
				}
			}
			p := newPrinter(g.json)
			out := struct {
				Version  string `json:"version"`
				Revision string `json:"revision,omitempty"`
			}{version, rev}
			p.human("osctf %s %s", out.Version, out.Revision)
			return p.data(out)
		},
	}
}
