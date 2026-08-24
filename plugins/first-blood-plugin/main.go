// A reference OSCTF scoring plugin: a FIRST-BLOOD bonus. The first solver locks in a bonus on top
// of the initial value; everyone after gets a linear decay by solve count. It is PURE — no config,
// no logging, no I/O — the whole scoring policy is a function of the Score the host passes.
//
// Why a first-blood bonus, and not linear decay? The built-in `dynamic` engine already does
// solve-count decay (CTFd-style), so a decay plugin would only reimplement a built-in. A
// first-blood bonus is a policy the core deliberately does NOT offer — and it is meaningful
// precisely because scoring is LOCKED AT SOLVE: the first solver's bonus is permanent, later solves
// never erode it. That design fact is what this example is built to teach.
package main

import "github.com/swayyaam/OSCTF/plugin/sdk"

// firstBloodBonusPct is the first solver's bonus, as a percent of the initial value. It is fixed
// (a scoring plugin receives no per-challenge params — see sdk.Score.Params) and scaled by Initial
// so it is proportional per challenge. A different policy would tune this via sdk.Config (per
// plugin) — this example needs neither, which is the point of a PURE scorer.
const firstBloodBonusPct = 50

type engine struct{}

func (engine) Info() sdk.Info { return sdk.Info{Name: "first-blood", Version: "0.1.0"} }

// Value: everyone decays linearly by solve count down to Min, and the FIRST solver (Solves == 1,
// which includes their own solve) locks in a bonus on top. Because scoring is locked at solve, that
// bonus stays with the first solver forever. Pure and deterministic.
func (engine) Value(in sdk.Score) int {
	v := in.Initial - (in.Solves-1)*in.Decay
	if v < in.Min {
		v = in.Min
	}
	if in.Solves <= 1 {
		v += in.Initial * firstBloodBonusPct / 100
	}
	return v
}

func main() { sdk.Serve(sdk.Scoring, engine{}) }
