package sdk

import (
	"context"
	"testing"

	"github.com/osctf/platform/internal/plugin"
	"github.com/osctf/platform/internal/plugin/pluginpb"
)

// linear is a minimal Scorer for the white-box adapter tests.
type linear struct{}

func (linear) Info() Info { return Info{Name: "linear", Version: "9.9.9"} }
func (linear) Value(s Score) int {
	v := s.Initial - s.Solves*s.Decay
	if v < s.Min {
		return s.Min
	}
	return v
}

// The adapter must translate the wire request into a plain-Go Score and back, with no protobuf
// visible to the Scorer.
func TestScoringAdapterTranslatesValue(t *testing.T) {
	a := &scoringAdapter{impl: linear{}}
	resp, err := a.Value(context.Background(), &pluginpb.ScoreRequest{Initial: 500, Min: 100, Decay: 50, Solves: 3})
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if got := resp.GetValue(); got != 350 {
		t.Errorf("Value = %d, want 350", got)
	}
}

// The SDK — not the author — owns Type and ABI: the adapter stamps them so a plugin cannot
// misdeclare the contract it speaks. The author's Info carries neither field.
func TestScoringAdapterStampsTypeAndABI(t *testing.T) {
	a := &scoringAdapter{impl: linear{}}
	info, err := a.Info(context.Background(), &pluginpb.InfoRequest{})
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if info.GetType() != pluginpb.PluginType_PLUGIN_TYPE_SCORING {
		t.Errorf("Type = %v, want SCORING (SDK-stamped from the Serve argument)", info.GetType())
	}
	if info.GetAbi() != plugin.ABIString {
		t.Errorf("Abi = %q, want the SDK's %q (SDK-stamped, not author-set)", info.GetAbi(), plugin.ABIString)
	}
	if info.GetName() != "linear" || info.GetVersion() != "9.9.9" {
		t.Errorf("Name/Version not carried through: %q %q", info.GetName(), info.GetVersion())
	}
}

// Serve must reject an impl that does not satisfy the type's interface, at once and loudly,
// rather than starting a plugin that serves nothing.
func TestServeRejectsWrongImpl(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Serve(Scoring, notAScorer) should panic")
		}
	}()
	Serve(Scoring, struct{}{})
}
