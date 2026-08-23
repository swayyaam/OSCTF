package contract_test

import (
	"testing"

	"github.com/swayyaam/OSCTF/plugin/sdk"
	"github.com/swayyaam/OSCTF/plugin/sdk/contract"
)

// End-to-end proof of the entire author path using ONLY the public API: an example plugin that
// imports plugin/sdk is built and verified through plugin/sdk/contract — exactly what an
// out-of-tree author does, with no internal/* anywhere in this file.
func TestVerifyScoring_Example(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin subprocess; skipped in -short")
	}
	bin := contract.Build(t, "testdata/examplescorer")
	contract.VerifyScoring(t, bin, []contract.ScoringCase{
		{Name: "no solves", In: sdk.Score{Initial: 500, Min: 100, Decay: 50, Solves: 0}, Want: 500},
		{Name: "some decay", In: sdk.Score{Initial: 500, Min: 100, Decay: 50, Solves: 3}, Want: 350},
		{Name: "clamped at floor", In: sdk.Score{Initial: 500, Min: 100, Decay: 50, Solves: 20}, Want: 100},
	})
}

func TestVerifyNotification_Example(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin subprocess; skipped in -short")
	}
	bin := contract.Build(t, "testdata/examplenotifier")
	contract.VerifyNotification(t, bin, []contract.NotificationCase{
		{Name: "solved", Event: sdk.Event{Name: "challenge.solved", ID: "e1", Data: map[string]string{"team": "alpha"}}},
	})
}

func TestVerifyChallengeType_Example(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a plugin subprocess; skipped in -short")
	}
	bin := contract.Build(t, "testdata/examplechecker")
	contract.VerifyChallengeType(t, bin, contract.ChallengeTypeCases{
		ValidConfig:     map[string]string{"answer": "  OSCTF{win}  "}, // normalized (trimmed) → OSCTF{win}
		RejectedConfigs: []map[string]string{{}, {"answer": "   "}},    // missing / blank → per-field error
		Correct:         []string{"OSCTF{win}"},                        // equals the normalized answer
		Incorrect:       []string{"OSCTF{nope}", "  OSCTF{win}  "},     // wrong, incl. the un-trimmed form (checked vs normalized)
		Undecidable:     []string{""},                                  // blank → CheckFlag errors (fail closed), not false
	})
}
