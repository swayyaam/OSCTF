// Double: HANG — Value never returns (and ignores the context). Proves the host never blocks
// indefinitely: the host-side deadline must fire and free the caller.
package main

import (
	"context"

	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
	"github.com/swayyaam/OSCTF/internal/plugin/plugintest"
)

type hang struct{ plugintest.OKScoring }

func (hang) Value(context.Context, *pluginpb.ScoreRequest) (*pluginpb.ScoreResponse, error) {
	select {} // block forever
}

func main() { plugintest.ServeScoring(hang{plugintest.OKScoring{Name: "hang"}}) }
