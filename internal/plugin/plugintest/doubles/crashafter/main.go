// Double: CRASHES AFTER SERVING — handshakes and answers Info, then exits non-zero on the
// first Value call. Distinct from crash-on-launch: it runs, gets registered, THEN dies — the
// "serves for T seconds then crashes" crash-loop the restart policy must still quarantine
// (~5·T longer than a launch crash), and the in-flight call must resolve as a clear error.
package main

import (
	"context"
	"os"

	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
	"github.com/swayyaam/OSCTF/internal/plugin/plugintest"
)

type crashAfter struct{ plugintest.OKScoring }

func (crashAfter) Value(context.Context, *pluginpb.ScoreRequest) (*pluginpb.ScoreResponse, error) {
	os.Exit(1) // die mid-call
	return nil, nil
}

func main() { plugintest.ServeScoring(crashAfter{plugintest.OKScoring{Name: "crashafter"}}) }
