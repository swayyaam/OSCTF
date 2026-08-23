// Package plugintest provides the hostile plugin doubles and the build/dial harness they are
// exercised through. The doubles are REAL plugin binaries (separate processes over the OSCTF
// go-plugin handshake) that misbehave in specific, named ways — crash on launch, hang, crash
// after serving, return malformed responses, ignore shutdown, respond correctly-but-slowly,
// and stall on shutdown past the drain window. They are the fixtures the loader (P3-c) and
// failure isolation (P3-d) are built against, so a plugin outage is tested against a plugin
// that actually outages, not a mock.
//
// This file is the PLUGIN side (used by the double main packages). harness.go is the HOST
// side (build + dial), used by the tests.
package plugintest

import (
	"context"

	goplugin "github.com/hashicorp/go-plugin"

	"github.com/swayyaam/OSCTF/internal/plugin"
	"github.com/swayyaam/OSCTF/internal/plugin/pluginpb"
)

// ServeScoring runs a Scoring plugin over the OSCTF handshake. Blocks until the host
// disconnects (or the process is killed). The doubles call this from main.
func ServeScoring(impl pluginpb.ScoringServer) {
	ServeScoringHandshake(impl, plugin.Handshake)
}

// ServeScoringHandshake is ServeScoring with an explicit handshake — used by the wrong-ABI
// double, which serves a different ProtocolVersion so the host must refuse it pre-call.
func ServeScoringHandshake(impl pluginpb.ScoringServer, hs goplugin.HandshakeConfig) {
	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: hs,
		Plugins:         goplugin.PluginSet{plugin.KeyScoring: &plugin.ScoringGRPCPlugin{Impl: impl}},
		GRPCServer:      goplugin.DefaultGRPCServer,
	})
}

// OKScoring is a well-behaved, deterministic Scoring impl the doubles embed and selectively
// override. Value returns a linear curve; Info advertises the current ABI.
type OKScoring struct {
	pluginpb.UnimplementedScoringServer
	Name string // defaults to "double" via Info if empty
}

func (s OKScoring) Info(context.Context, *pluginpb.InfoRequest) (*pluginpb.InfoResponse, error) {
	name := s.Name
	if name == "" {
		name = "double"
	}
	return &pluginpb.InfoResponse{
		Name:    name,
		Type:    pluginpb.PluginType_PLUGIN_TYPE_SCORING,
		Abi:     plugin.ABIString,
		Version: "0.0.0",
	}, nil
}

func (OKScoring) Value(_ context.Context, req *pluginpb.ScoreRequest) (*pluginpb.ScoreResponse, error) {
	v := req.GetInitial() - req.GetSolves()*req.GetDecay()
	if v < req.GetMin() {
		v = req.GetMin()
	}
	return &pluginpb.ScoreResponse{Value: v}, nil
}
