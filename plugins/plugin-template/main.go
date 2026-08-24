// A working OSCTF plugin. As shipped it is a SCORING plugin: a linear-decay curve.
//
// To make it YOUR plugin, change three things:
//  1. Info() below — the Name (how the host registers it) and Version.
//  2. Value() — your scoring logic. (Or switch to another plugin type; see README.md → "Change
//     the plugin type".)
//  3. plugin.yaml — the name/type/description to match.
//
// Everything else — the go-plugin handshake, the gRPC transport, the ABI wire format — is the
// SDK's job. You never import go-plugin, gRPC, or protobuf.
package main

import "github.com/swayyaam/OSCTF/plugin/sdk"

// engine is your plugin. A scoring plugin implements sdk.Scorer: Info + Value.
type engine struct{}

// Info identifies the plugin. Name is the manifest name the host registers it under; Version is
// your own release. The plugin TYPE (from Serve, below) and the ABI (a fact of the SDK you build
// against) are owned by the SDK — you do not, and cannot, declare them here, so they cannot be
// misdeclared.
func (engine) Info() sdk.Info {
	return sdk.Info{Name: "my-plugin", Version: "0.1.0"}
}

// Value scores a challenge. It is PURE: the same Score in must yield the same value out, with no
// I/O. Here it decays linearly by the challenge's configured Decay step and clamps at Min.
func (engine) Value(in sdk.Score) int {
	// LOCKED AT SOLVE: Value runs ONCE per solve and is recorded — it is not re-evaluated, so
	// earlier solvers keep the value they were given (a decay curve sets what each solver locks in
	// at their solve, it does not retroactively lower them). in.Solves includes this solve, so the
	// first solver sees 1. See sdk.Scorer.
	//
	// If your plugin needs CONFIG, read it from the manifest via sdk.Config() (declare the key in
	// plugin.yaml; a secret resolves from the host environment only):
	//   step := sdk.Config().Int("step")
	//
	// To LOG, use sdk.Log() — output reaches the host's logs, tagged as your plugin. NEVER log a
	// secret, a config value, an event payload, or a flag:
	//   sdk.Log().Debug("scoring", "solves", in.Solves)
	//
	// (A scoring plugin is pure, so it needs neither — these show how when your plugin does.)
	v := in.Initial - in.Solves*in.Decay
	if v < in.Min {
		return in.Min
	}
	return v
}

func main() { sdk.Serve(sdk.Scoring, engine{}) }
