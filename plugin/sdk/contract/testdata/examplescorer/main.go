// Command examplescorer is a minimal scoring plugin used by the contract-harness test. It is
// also the smallest honest example of the whole author surface: implement sdk.Scorer, call
// sdk.Serve. Nothing else — no go-plugin, no gRPC, no protobuf.
package main

import "github.com/swayyaam/OSCTF/plugin/sdk"

type engine struct{}

func (engine) Info() sdk.Info {
	return sdk.Info{Name: "example-linear", Version: "0.1.0"}
}

// Value is a linear decay clamped at Min — pure, deterministic, no I/O.
func (engine) Value(in sdk.Score) int {
	v := in.Initial - in.Solves*in.Decay
	if v < in.Min {
		return in.Min
	}
	return v
}

func main() { sdk.Serve(sdk.Scoring, engine{}) }
