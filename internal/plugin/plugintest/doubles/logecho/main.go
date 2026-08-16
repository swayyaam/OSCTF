// Double: logs via the PUBLIC sdk.Log() on each call, so the plugin→host log path (over
// go-plugin's stderr channel into the host Logger) can be asserted end to end.
package main

import "github.com/osctf/platform/plugin/sdk"

type engine struct{}

func (engine) Info() sdk.Info { return sdk.Info{Name: "logecho", Version: "0.0.0"} }

func (engine) Value(in sdk.Score) int {
	sdk.Log().Info("scored-a-challenge", "solves", in.Solves)
	return in.Initial
}

func main() { sdk.Serve(sdk.Scoring, engine{}) }
