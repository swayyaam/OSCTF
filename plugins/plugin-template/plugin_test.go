package main

import (
	"testing"

	"github.com/swayyaam/OSCTF/plugin/sdk"
	"github.com/swayyaam/OSCTF/plugin/sdk/contract"
)

// TestPluginContract builds this plugin and dials it exactly as the host does, then checks it
// satisfies the OSCTF scoring contract. This answers "is my plugin correct?" WITHOUT the platform
// repo — the contract harness is part of the public SDK.
//
// Edit the cases to match your Value() logic. If you switch plugin types, swap VerifyScoring for
// the matching verifier (e.g. contract.VerifyNotification).
func TestPluginContract(t *testing.T) {
	bin := contract.Build(t, ".")
	contract.VerifyScoring(t, bin, []contract.ScoringCase{
		{Name: "no solves", In: sdk.Score{Initial: 500, Min: 100, Decay: 50, Solves: 0}, Want: 500},
		{Name: "some decay", In: sdk.Score{Initial: 500, Min: 100, Decay: 50, Solves: 3}, Want: 350},
		{Name: "clamped at floor", In: sdk.Score{Initial: 500, Min: 100, Decay: 50, Solves: 20}, Want: 100},
	})
}
