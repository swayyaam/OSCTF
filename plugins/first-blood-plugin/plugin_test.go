package main

import (
	"testing"

	"github.com/swayyaam/OSCTF/plugin/sdk"
	"github.com/swayyaam/OSCTF/plugin/sdk/contract"
)

func TestPluginContract(t *testing.T) {
	bin := contract.Build(t, ".")
	contract.VerifyScoring(t, bin, []contract.ScoringCase{
		{Name: "first blood: Initial + 50%", In: sdk.Score{Initial: 500, Min: 100, Decay: 50, Solves: 1}, Want: 750},
		{Name: "second solver: one decay step", In: sdk.Score{Initial: 500, Min: 100, Decay: 50, Solves: 2}, Want: 450},
		{Name: "later solver: clamped at floor", In: sdk.Score{Initial: 500, Min: 100, Decay: 50, Solves: 20}, Want: 100},
	})
}
