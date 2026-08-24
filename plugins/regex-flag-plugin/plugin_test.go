package main

import (
	"testing"

	"github.com/swayyaam/OSCTF/plugin/sdk"
	"github.com/swayyaam/OSCTF/plugin/sdk/contract"
)

// TestPluginContract builds this plugin and dials it exactly as the host does, then checks it
// satisfies the OSCTF challenge-type contract — WITHOUT the platform repo (the harness is part of
// the public SDK). VerifyChallengeType asserts the behaviour a correctness-deciding plugin must get
// right: ValidateConfig accepts valid config and rejects invalid config with a per-field error,
// CheckFlag is deterministic, and the flag checks run against the NORMALIZED config the plugin
// returned (what the host stores and later hands back).
func TestPluginContract(t *testing.T) {
	bin := contract.Build(t, ".")
	contract.VerifyChallengeType(t, bin, contract.ChallengeTypeCases{
		ValidConfig: map[string]string{"pattern": `^OSCTF\{[a-z0-9_]+\}$`},
		RejectedConfigs: []map[string]string{
			{},                     // no pattern → per-field error on "pattern"
			{"pattern": "OSCTF{("}, // unbalanced group → not a valid regex → per-field error
		},
		Correct:   []string{"OSCTF{sql_injection}", "OSCTF{xss}"},
		Incorrect: []string{"OSCTF{WRONG}", "not-a-flag", "OSCTF{has space}", "prefixOSCTF{x}"},
		// A compiled regex ALWAYS decides a submission, so with a valid pattern there is no
		// submission this checker cannot decide — hence no Undecidable cases here. The "cannot
		// decide" path (a missing/broken stored pattern) is a CONFIG failure, not reachable through a
		// submission against a valid config, so it is proven directly below rather than contrived
		// into a fake undecidable submission.
		Undecidable: nil,
	})
}

// TestCheckFlagFailsClosedWithoutPattern proves the single most consequential property directly,
// because VerifyChallengeType runs CheckFlag against the VALID normalized config and so never
// reaches the checker's cannot-decide path. When there is no usable pattern the checker must return
// an ERROR (the host fails closed and burns no attempt), never false (which would silently cost the
// player a try for a config fault they didn't cause).
func TestCheckFlagFailsClosedWithoutPattern(t *testing.T) {
	for name, cfg := range map[string]map[string]string{
		"no pattern key": {},
		"empty pattern":  {"pattern": ""},
	} {
		correct, err := checker{}.CheckFlag(sdk.FlagCheck{Submitted: "OSCTF{x}", Config: cfg})
		if err == nil {
			t.Errorf("%s: CheckFlag returned (correct=%v, err=nil); it must FAIL CLOSED with an error, not decide — a false here burns the player's attempt", name, correct)
		}
	}
}
