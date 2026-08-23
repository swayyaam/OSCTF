// Double: well-behaved baseline. Serves correctly and promptly — the control the misbehaving
// doubles are compared against.
//
// Built through the PUBLIC plugin/sdk (not internal/plugintest), so the loader and
// failure-isolation suites that dial this double double as proof that an SDK-built plugin is
// loader-compatible: if the public author surface ever stopped producing a real OSCTF plugin,
// these tests would go red. The linear curve matches plugintest.OKScoring so its wire behaviour
// is unchanged.
package main

import "github.com/swayyaam/OSCTF/plugin/sdk"

type engine struct{}

func (engine) Info() sdk.Info { return sdk.Info{Name: "goodscore", Version: "0.0.0"} }

func (engine) Value(in sdk.Score) int {
	v := in.Initial - in.Solves*in.Decay
	if v < in.Min {
		return in.Min
	}
	return v
}

func main() { sdk.Serve(sdk.Scoring, engine{}) }
