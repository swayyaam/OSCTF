package sdk

import (
	"context"
	"math"

	"github.com/osctf/platform/internal/plugin/pluginpb"
)

// Score is the input to a scoring computation — the plain-Go mirror of the wire request. A
// scoring plugin is PURE: the same Score in yields the same value out, with no I/O.
type Score struct {
	Initial int               // configured initial points
	Min     int               // floor the value never drops below
	Decay   int               // per-solve decay step (from challenge config)
	Solves  int               // valid solves so far
	Params  map[string]string // challenge-scoped scoring params (author-defined keys)
}

// Scorer is implemented by a scoring plugin.
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
