package sdk

import (
	"context"
	"math"

	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
)

// Score is the input to a scoring computation — the plain-Go mirror of the wire request.
type Score struct {
	Initial int // configured initial points
	Min     int // floor the value never drops below
	Decay   int // per-solve decay step (from challenge config)
	Solves  int // valid solves so far, INCLUDING this solve — so the first solver sees Solves == 1
	// Params is RESERVED and ALWAYS EMPTY in v0.3 — do not read it. There is no per-challenge
	// scoring-params source today: the host has no column to hold author-defined scoring params, and
	// the scoring service has no validation RPC to check them at challenge-write time, so nothing is
	// ever put here. Populating it is a v0.4 candidate — it needs a schema change plus a write-time
	// validation path, not a workaround — so treat this field as absent, not merely unset.
	//
	// What you DO have: a scoring plugin is configured PER-DEPLOYMENT via sdk.Config — one set of
	// values for the whole plugin, from the manifest and host env — NOT per-challenge. Its only
	// per-challenge inputs are Initial/Min/Decay/Solves above. Tune with those four, a fixed rule,
	// or sdk.Config. Never Params.
	Params map[string]string
}

// Scorer is implemented by a scoring plugin.
//
// LOCKED AT SOLVE — the one thing to know before writing Value. Value is called EXACTLY ONCE per
// solve, at that solve, and the result is RECORDED on the solve. It is not re-evaluated: when a
// later team solves, earlier solvers keep the value they were given. So a decay curve does not
// retroactively lower an early solver — it sets what each solver locks in at the moment they solve
// (Solves is the count at that instant). Value is off the read path (the board reads the record,
// never the plugin), which is why it must be PURE: same Score in, same value out, no I/O.
//
// NO TIME INPUT — deliberate, not an oversight. Score carries solve ORDER (Solves), never a clock.
// Value must be a pure function of (Initial, Min, Decay, Solves) so the recorded value stays
// reproducible from the solve log alone. A time-dependent curve — time-based decay, solve-rate, or
// "first blood within N minutes" — would make the recorded value un-recomputable from the record,
// so it is excluded by design. Time-aware scoring is not a missing argument you can add later; it
// requires extending the recorded-value model, and is out of scope until that exists.
type Scorer interface {
	Info() Info
	Value(Score) int
}

// scoringAdapter translates the generated gRPC ScoringServer into calls on the author's Scorer.
// This is where the wire types live and die; nothing above pluginpb crosses into author code.
type scoringAdapter struct {
	pluginpb.UnimplementedScoringServer
	impl Scorer
}

func (a *scoringAdapter) Info(context.Context, *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error) {
	return infoResponse(a.impl.Info(), pluginpb.PluginType_PLUGIN_TYPE_SCORING), nil
}

func (a *scoringAdapter) Value(_ context.Context, req *pluginpb.ScoreRequest) (*pluginpb.ScoreResponse, error) {
	v := a.impl.Value(Score{
		Initial: int(req.GetInitial()),
		Min:     int(req.GetMin()),
		Decay:   int(req.GetDecay()),
		Solves:  int(req.GetSolves()),
		Params:  req.GetParams(),
	})
	return &pluginpb.ScoreResponse{Value: asInt32(v)}, nil
}

// asInt32 saturates an int to the int32 wire range. Scoring values are small (bounded by the
// challenge's own int32 config), so this never triggers in practice; saturating rather than
// wrapping means even a pathological author value degrades to a bounded number, not a
// sign-flipped one.
func asInt32(v int) int32 {
	switch {
	case v > math.MaxInt32:
		return math.MaxInt32
	case v < math.MinInt32:
		return math.MinInt32
	}
	return int32(v) //nolint:gosec // G115: bounds checked immediately above.
}
